package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"

	"hyperhand/internal/proto"
)

// hashFiles returns the SHA-256 of each path; "" for a missing, directory or unreadable path.
func hashFiles(ctx context.Context, args json.RawMessage, _ []byte) (any, []byte, error) {
	var a proto.PathsArgs
	if err := decode(args, &a); err != nil {
		return nil, nil, err
	}
	res := proto.HashesResult{Hashes: make([]string, len(a.Paths))}
	buf := make([]byte, 1<<20)
	for i, p := range a.Paths {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		res.Hashes[i] = hashFile(ctx, p, buf)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return res, nil, nil
}

func hashFile(ctx context.Context, path string, buf []byte) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || st.IsDir() {
		return ""
	}
	h := sha256.New()
	if _, err := io.CopyBuffer(h, ctxReader{ctx, f}, buf); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ctxReader stops a long read once ctx is cancelled.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
