# capture-extlink

A tool for capturing artifacts created during the external linking of a Go
program. Intended as a development/debugging tool for Go toolchain developers.

You give it a "go build" or "go test" command line and it re-executes the build,
capturing the intermediate files (Go, C, object) into an intermediate directory.

Usage:

```
% go build ... 
% capture-extlink -tag abc !!
... objects and intermediates captured to /tmp/artifacts.abc
% 
```

Notes:

* this tool runs "go clean -cache" as part of the rebuild
* artifact directory is removed/overwritten
* objects and generated intermediates (*.c, *.h, *.go) will be copied
  from the go cmd "work" dir to the artifact dir
  
  



