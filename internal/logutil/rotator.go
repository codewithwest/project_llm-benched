package logutil

import (
	"io"
	"os"
)

type RotatingWriter struct {
	path    string
	maxSize int64
	file    *os.File
}

func NewRotatingWriter(path string, maxSize int64) (*RotatingWriter, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	rw := &RotatingWriter{path: path, maxSize: maxSize, file: f}
	if fi, err := f.Stat(); err == nil && fi.Size() >= maxSize {
		rw.rotate()
	}
	return rw, nil
}

func (w *RotatingWriter) Write(p []byte) (int, error) {
	fi, err := w.file.Stat()
	if err == nil && fi.Size()+int64(len(p)) >= w.maxSize {
		w.rotate()
	}
	return w.file.Write(p)
}

func (w *RotatingWriter) Close() error {
	return w.file.Close()
}

func (w *RotatingWriter) rotate() {
	w.file.Close()
	os.Rename(w.path, w.path+".old")
	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		panic(err)
	}
	w.file = f
}

var _ io.Writer = (*RotatingWriter)(nil)
