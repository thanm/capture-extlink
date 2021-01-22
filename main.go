// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

var verbflag = flag.Int("v", 0, "Verbose trace output level")
var tagflag = flag.String("tag", "", "Tag to use for artifact dir")

func verb(vlevel int, s string, a ...interface{}) {
	if *verbflag >= vlevel {
		fmt.Printf(s, a...)
		fmt.Printf("\n")
	}
}

func warn(s string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, s, a...)
	fmt.Fprintf(os.Stderr, "\n")
}

func fatal(s string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, s, a...)
	fmt.Fprintf(os.Stderr, "\n")
	os.Exit(1)
}

func usage(msg string) {
	if len(msg) > 0 {
		fmt.Fprintf(os.Stderr, "error: %s\n", msg)
	}
	fmt.Fprintf(os.Stderr, "usage: capture-extlink [flags] -- <go build/go test>>\n")
	flag.PrintDefaults()
	os.Exit(2)
}

func docmd(cmd []string) {
	verb(1, "docmd: %s", strings.Join(cmd, " "))
	c := exec.Command(cmd[0], cmd[1:]...)
	b, err := c.CombinedOutput()
	if err != nil {
		fatal("error executing cmd %s: %v",
			strings.Join(cmd, " "), err)
	}
	os.Stderr.Write(b)
}

func docmdout(cmd []string, outfile string) {
	verb(1, "docmdout: %s > %s", strings.Join(cmd, " "), outfile)
	of, err := os.OpenFile(outfile, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		fatal("opening tmp outputfile %s: %v", outfile, err)
	}
	c := exec.Command(cmd[0], cmd[1:]...)
	c.Stdout = of
	c.Stderr = of
	err = c.Run()
	of.Close()
	if err != nil {
		fatal("error executing cmd %s: %v",
			strings.Join(cmd, " "), err)
	}
}

func findldflags(cmd []string) (int, string) {
	for i := 0; i < len(cmd); i++ {
		ldf := cmd[i]
		if strings.HasPrefix(cmd[i], "-ldflags=") {
			return i, ldf[len("-ldflags="):]
		}
	}
	return -1, ""
}

func dumpObject(path string, outfile string) {
	// Is this a Go object or a host/syso object?
	b, err := ioutil.ReadFile(path)
	if err != nil {
		fatal("reading object file %s failed: %v", path, err)
	}
	if !bytes.Contains(b, []byte("go object ")) {
		docmdout([]string{"go", "tool", "objdump", path}, outfile)
	} else {
		docmdout([]string{"objdump", "-t", path}, outfile)
	}
}

func perform(cmd []string) {
	// Remove and recreate artifact dir
	artdir := fmt.Sprintf("/tmp/xxx.%s", *tagflag)
	verb(1, "recreating artifact dir %s", artdir)
	if err := os.RemoveAll(artdir); err != nil {
		fatal("can't remove %s: %v", artdir, err)
	}
	if err := os.Mkdir(artdir, 0777); err != nil {
		fatal("can't create %s: %v", artdir, err)
	}

	// Cache clean
	docmd([]string{"go", "clean", "-cache"})

	// Construct rebuild cmd.
	exefile := fmt.Sprintf("%s/%s.exe", artdir, *tagflag)
	rcmd := []string{cmd[0], cmd[1], "-x", "-work", "-i", "-o", exefile}
	if slot, arg := findldflags(cmd); slot != -1 {
		cmd[slot] = fmt.Sprintf("-ldflags=-tmpdir=%s %s", artdir, arg)
	} else {
		rcmd = append(rcmd, fmt.Sprintf("-ldflags=-tmpdir=%s", artdir))
	}
	rcmd = append(rcmd, cmd[2:]...)

	// Now run build.
	errfile := fmt.Sprintf("%s/err.%s.txt", artdir, *tagflag)
	f, err := os.OpenFile(errfile, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		fatal("opening output file %s for build: %v", errfile, err)
	}
	verb(1, "cmd is: %s", strings.Join(rcmd, " "))
	ec := exec.Command(rcmd[0], rcmd[1:]...)
	ec.Stdout = f
	ec.Stderr = f
	ec.Run()
	verb(1, "build/test complete, output in %s", errfile)

	// Open and examine the build transcript, so as to pick out
	// the work dir.
	ef, err := os.Open(errfile)
	if err != nil {
		fatal("opening %s: %v", errfile, err)
	}
	wd := ""
	scanner := bufio.NewScanner(ef)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "WORK=") {
			chunks := strings.Split(line, "=")
			wd = chunks[1]
		}
	}
	ef.Close()
	verb(1, "workdir is: %s", wd)

	files := make(map[string]bool)
	visitor := func(path string, info os.FileInfo, err error) error {
		if err != nil {
			verb(0, "workdir %s walk: at %s: %v", wd, path, err)
			return err
		}
		if info.IsDir() {
			return nil
		}
		n := info.Name()
		if strings.HasSuffix(n, ".go") || strings.HasSuffix(n, ".c") ||
			strings.HasSuffix(n, ".h") || strings.HasSuffix(n, ".o") {
			files[path] = true
		}
		return nil
	}

	// Explore the workdir and pick out files to copy into the
	// artifact dir.
	err = filepath.Walk(wd, visitor)
	if err != nil {
		fatal("%v", err)
	}
	paths := []string{}
	for path := range files {
		verb(1, "workdir path %s", path)
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for path := range files {
		chunks := strings.Split(path, "/")
		verb(1, "path %v", chunks)
		destdir := filepath.Join(artdir, chunks[len(chunks)-2])
		os.MkdirAll(destdir, 0777)
		destfile := filepath.Join(destdir, chunks[len(chunks)-1])
		copyfile(path, destfile)
	}

	dumpvisitor := func(path string, info os.FileInfo, err error) error {
		if err != nil {
			verb(0, "artifact dir %s walk: at %s: %v", wd, path, err)
			return err
		}
		if info.IsDir() {
			return nil
		}
		n := info.Name()
		if strings.HasSuffix(n, ".o") {
			odout := path[:len(path)-2] + ".od.txt"
			docmdout([]string{"objdump", "-t", path}, odout)
		}
		return nil
	}
	// Now that we've copied in objects from the workdir, run
	// "objdump -t" on all objects in the artifact dir.
	err = filepath.Walk(artdir, dumpvisitor)
	if err != nil {
		fatal("%v", err)
	}
}

func copyfile(from string, to string) {
	input, err := ioutil.ReadFile(from)
	if err != nil {
		fatal("copying %s: readfile %v", from, err)
	}
	err = ioutil.WriteFile(to, input, 0644)
	if err != nil {
		fatal("copying %s: writefile %s: %v", from, to, err)
	}
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("capture-extlink: ")
	flag.Parse()
	verb(1, "in main")
	if *tagflag == "" {
		usage("please supply tag name with -tag option")
	}
	args := flag.Args()
	if len(args) < 2 || args[0] != "go" ||
		(args[1] != "test" && args[1] != "build") {
		usage("please supply 'go build' or 'go test' command")
	}
	verb(1, "build/test command is: %s", strings.Join(args, " "))
	perform(args)
	verb(1, "leaving main")
}
