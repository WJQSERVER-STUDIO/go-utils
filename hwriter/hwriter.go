package hwriter

import (
	"fmt"
	"io"

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

		_, err = bw.Write(buf[:n]) // Use the chunked body writer
		if err != nil {
			return fmt.Errorf("failed to write chunk: %w", err)
		}

		err = bw.Flush() // Flush the chunk to the client
		if err != nil {
			return fmt.Errorf("failed to flush chunk: %w", err)
		}
	}

	return nil
}
