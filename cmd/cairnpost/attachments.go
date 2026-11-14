package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mettjs/cairnpost/internal/config"
	"github.com/mettjs/cairnpost/internal/driver/capture"
	"github.com/mettjs/cairnpost/internal/mail"
)

// readTimeout is how long a request may take to arrive whole.
const readTimeout = 30 * time.Second

// attachmentsOf is how many files an email may carry, as the mail
// service is told: none is said as a negative number, 0 being its
// word for "the default".
func attachmentsOf(cfg config.Config) int {
	if cfg.MaxAttachments == 0 {
		return -1
	}
	return cfg.MaxAttachments
}

// emailBytes is how large a send's body may be: the body any request
// may have, and its files as they travel, in base64, with room for each
// one's name and type.
func emailBytes(cfg config.Config) int64 {
	if cfg.MaxAttachments == 0 {
		return cfg.MaxBodyBytes
	}
	return cfg.MaxBodyBytes + int64(base64.StdEncoding.EncodedLen(int(cfg.MaxAttachmentBytes))) + int64(cfg.MaxAttachments)*1024
}

// filesOf says what a captured email's files were.
func filesOf(held []capture.File) []mail.File {
	var out []mail.File
	for _, f := range held {
		out = append(out, mail.File{Filename: f.Filename, ContentType: f.Type, ContentID: f.ContentID, Size: f.Size})
	}
	return out
}

// paths is a flag given once for each file.
type paths []string

func (p *paths) String() string     { return fmt.Sprint([]string(*p)) }
func (p *paths) Set(v string) error { *p = append(*p, v); return nil }

// attachLargest is the most the command reads of one file: what the
// largest setting of a server takes.
const attachLargest = 64 << 20

// attached reads the files a send names into what the API takes: each
// by its own name, without the folders it is in. nil for none, so the
// request says nothing of files.
func attached(files paths) ([]map[string]string, error) {
	var out []map[string]string
	for _, file := range files {
		info, err := os.Stat(file)
		switch {
		case err != nil:
			return nil, fmt.Errorf("the file to attach could not be read: %w", err)
		case !info.Mode().IsRegular():
			return nil, fmt.Errorf("%s is not a file", file)
		case info.Size() > attachLargest:
			return nil, fmt.Errorf("%s is larger than any server takes (%d bytes)", file, attachLargest)
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("the file to attach could not be read: %w", err)
		}
		out = append(out, map[string]string{"filename": filepath.Base(file), "content": base64.StdEncoding.EncodeToString(raw)})
	}
	return out, nil
}
