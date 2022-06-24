package main

import (
	"go/ast"
	"go/token"
	"io"
	"os"

	"github.com/sirupsen/logrus"
)

type fileCache map[string][]byte

func (cache fileCache) readNodeSource(fset *token.FileSet, node ast.Node, limit int) (string, int, string) {
	file := fset.File(node.Pos())
	if file == nil {
		logrus.Debugf("Unable to get file for node %v", node)
		return "", -1, ""
	}

	start := file.Offset(node.Pos())

	// end is first char after the node, so check if we're at EOF
	var end int
	if int(node.End())-file.Base() < file.Size() {
		end = file.Offset(node.End())
	} else {
		end = file.Size()
	}

	var contents []byte
	var ok bool
	if contents, ok = cache[file.Name()]; !ok {
		fh, err := os.Open(file.Name())
		if err != nil {
			logrus.Infof("Unable to open file %q to get source: %v", file.Name(), err)
			return "", -1, ""
		}
		defer fh.Close()

		contents, err = io.ReadAll(fh)
		if err != nil {
			logrus.Infof("Unable to read from file %q to get source: %v", end-start, file.Name(), err)
			return "", -1, ""
		}

		cache[file.Name()] = contents
	}

	size := end - start
	if limit > 0 && size > limit {
		size = limit
	}

	return file.Name(), start, string(contents[start : start+size])
}
