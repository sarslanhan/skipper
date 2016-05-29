/*
Package serve provides utilities for filters that need to modify the response body.
*/
package serve

import (
	"io"
	"net/http"

	"github.com/zalando/skipper/filters"
)

// A PipeBody can be used to stream data from filters. To get
// an initialized instance, use NewPipedBody().
type PipedBody struct {
	reader       io.ReadCloser
	writer       *io.PipeWriter
	closed       chan struct{}
	writerClosed chan struct{}
}

type pipedResponse struct {
	response   *http.Response
	body       *PipedBody
	headerDone chan struct{}
}

// NewPipedBody creates a body object, that can be
// used to stream content from filters. This object is
// based on io.Pipe. It is synchronized and does not
// use an internal buffer. The CloseWithError method
// calls the underlying PipeWriter's CloseWithError
// method.
//
// Example, gzip response:
//
// 	func (f *myFilter) Response(ctx filters.FilterContext) {
// 		in := ctx.Response().Body
// 		out := serve.NewPipedBody()
// 		ctx.Response().Body = out
//
// 		ctx.Response().Header.Del("Content-Lenght")
// 		ctx.Response().Header.Set("Content-Encoding", "gzip")
// 		ctx.Response().Header.Add("Vary", "Accept-Encoding")
//
// 		go func() {
// 			defer in.Close()
//
// 			gz := gzip.NewWriter(out)
// 			defer gz.Close()
//
// 			_, err := io.Copy(gz, in) // timeout handled through the original body
// 			if err == nil {
// 				err = io.EOF
// 			}
//
// 			out.CloseWithError(err)
// 		}()
// 	}
//
func NewPipedBody() *PipedBody {
	pr, pw := io.Pipe()
	return &PipedBody{
		reader:       pr,
		writer:       pw,
		closed:       make(chan struct{}),
		writerClosed: make(chan struct{})}
}

// io.Reader implementation.
func (b *PipedBody) Read(p []byte) (int, error) {
	return b.reader.Read(p)
}

// io.Writer implementation. If the writer side was
// closed then NOOP.
func (b *PipedBody) Write(p []byte) (int, error) {
	select {
	case <-b.writerClosed:
		return 0, nil
	default:
	}

	return b.writer.Write(p)
}

// CloseWithError closes the writer side of the pipe.
// It can be used to signal an io.EOF on the reader
// side.
func (b *PipedBody) CloseWithError(err error) {
	select {
	case <-b.writerClosed:
		return
	default:
	}

	b.writer.CloseWithError(err)
	close(b.writerClosed)
}

// Close closes the pipe. If the writer was not closed
// before, it signals an io.EOF.
func (b *PipedBody) Close() error {
	select {
	case <-b.closed:
		return nil
	default:
	}

	b.CloseWithError(io.EOF)
	b.reader.Close()
	close(b.closed)
	return nil
}

// Creates a response from a handler and a request.
//
// It calls the handler's ServeHTTP method with an internal response
// writer that shares the status code, headers and the response body
// with the returned response. It blocks until the handler calls the
// response writer's WriteHeader, or starts writing the body, or
// returns. The written body is not buffered, but piped to the returned
// response's body.
//
// Example, a simple file server:
//
// 	var handler = http.StripPrefix(webRoot, http.FileServer(http.Dir(root)))
//
// 	func (f *myFilter) Request(ctx filters.FilterContext) {
// 		serve.ServeHTTP(ctx, handler)
// 	}
//
func ServeHTTP(ctx filters.FilterContext, h http.Handler) {
	rsp := &http.Response{Header: make(http.Header)}
	body := NewPipedBody()
	d := &pipedResponse{
		response:   rsp,
		body:       body,
		headerDone: make(chan struct{})}

	req := ctx.Request()
	go func() {
		h.ServeHTTP(d, req)
		select {
		case <-d.headerDone:
		default:
			d.WriteHeader(http.StatusOK)
		}

		body.CloseWithError(io.EOF)
	}()

	<-d.headerDone
	rsp.Body = d
	ctx.Serve(rsp)
}

func (d *pipedResponse) Read(data []byte) (int, error) { return d.body.Read(data) }
func (d *pipedResponse) Header() http.Header           { return d.response.Header }

// Implements http.ResponseWriter.Write. When WriteHeader was
// not called before Write, it calls it with the default 200
// status code.
func (d *pipedResponse) Write(data []byte) (int, error) {
	select {
	case <-d.headerDone:
	default:
		d.WriteHeader(http.StatusOK)
	}

	return d.body.Write(data)
}

// It sets the status code for the outgoing response, and
// signals that the header is done.
func (d *pipedResponse) WriteHeader(status int) {
	d.response.StatusCode = status
	close(d.headerDone)
}

func (d *pipedResponse) Close() error {
	d.body.Close()
	return nil
}
