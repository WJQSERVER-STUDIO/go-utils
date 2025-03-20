package hwriter

import (
	"fmt"
	"io"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	hresp "github.com/cloudwego/hertz/pkg/protocol/http1/resp"
	"github.com/valyala/bytebufferpool"
)

func Writer(resp io.ReadCloser, c *app.RequestContext) error {
	defer resp.Close()

	bw := hresp.NewChunkedBodyWriter(&c.Response, c.GetWriter())
	c.Response.HijackWriter(bw)

	bufWrapper := bytebufferpool.Get()
	buf := bufWrapper.B
	size := 32768 // 32KB
	buf = buf[:cap(buf)]
	if len(buf) < size {
		buf = append(buf, make([]byte, size-len(buf))...)
	}
	buf = buf[:size] // 将缓冲区限制为 'size'
	defer bytebufferpool.Put(bufWrapper)

	for {
		n, err := resp.Read(buf)
		if err != nil {
			if err == io.EOF {
				break // 读取到文件末尾
			}
			return fmt.Errorf("failed to read response body: %w", err)
		}

		if n > 0 { // Only write if we actually read something
			_, err = bw.Write(buf[:n])
			if err != nil {
				// Handle write error (consider logging and potentially aborting)
				return fmt.Errorf("failed to write chunk: %w", err)
			}

			//Consider removing Flush in most case.  Only keep it if you *really* need it.
			if err := bw.Flush(); err != nil {
				// More robust error handling for Flush()
				c.AbortWithStatus(http.StatusInternalServerError) // Abort the response
				return fmt.Errorf("failed to flush chunk: %w", err)
			}
		}
	}

	return nil
}
