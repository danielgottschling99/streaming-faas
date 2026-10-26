# Writing a function

Functions are Go files compiled together with the supplied
[stream runtime](../docker/runtimes/go/functionhandlerStream.go). The server
packages them into a Docker image when you deploy. You do not need to write
your own Dockerfile or `main()`.

## Function directory

Create a directory such as `examples/my-function` containing `fn.go` and
`go.mod`. Keep the function's Go files at the top level, use `package main`,
and make dependencies compatible with the current Go 1.22 function image:

```go
// go.mod
module my-function

go 1.22
```

For framed images or other structured streams, register `OnStreamConnected`
and handle the TCP connection yourself. This minimal example forwards bytes
unchanged to the configured output:

```go
package main

import (
    "fmt"
    "io"
)

func init() { OnStreamConnected = forward }

// The runtime requires fn even when OnStreamConnected handles the stream.
func fn(body string, headers map[string]string) (string, error) {
    return "", fmt.Errorf("my-function requires a stream connection")
}

func forward(stream StreamConnection) error {
    buf := make([]byte, 32*1024)
    for {
        n, err := stream.Conn.Read(buf)
        if n > 0 {
            if outputErr := SendOutputBytes(buf[:n]); outputErr != nil {
                return outputErr
            }
        }
        if err == io.EOF {
            return nil
        }
        if err != nil {
            return err
        }
    }
}
```

The runtime closes the connection when the hook returns. Use `OnInit` for
optional startup setup. For simple text processing, you can omit the stream
hook and implement `fn` instead. It receives arbitrary chunks of up to 1,024
bytes, not complete messages.

## Using fn directly

The [uppercase example](uppercase/fn.go) implements only this hook:

```go
func fn(body string, headers map[string]string) (string, error) {
    return strings.ToUpper(body), nil
}
```

Without `OnStreamConnected`, the runtime calls `fn` for each TCP chunk and
sends its returned text to the output sink. Try it with ASCII text and netcat
(`nc`, included on macOS or available as `netcat-openbsd` on Linux).
Run these commands from the repository root:

```bash
# Terminal 1: server
make start
```

```bash
# Terminal 2: text output sink
nc -l 9100
```

```bash
# Terminal 3: text input source
nc -l 9000
```

```bash
# Terminal 4: deploy and connect
go run ./cmd/deploy -name uppercase -dir ./examples/uppercase \
  -env OUTPUT_HOST=host.docker.internal,OUTPUT_PORT=9100
make connect FUNCTION_NAME=uppercase
```

After connecting, type `hello world` and press Enter in terminal 3. Terminal 2
prints `HELLO WORLD`, and terminal 3 receives the runtime's `ack` response.
On Linux, replace `host.docker.internal` with your host IP and set
`CONNECT_HOST` as explained in the [main README](../README.md).
Use Ctrl+C to stop the listeners. These ports are shared with the image demos,
so stop any running demo source and sink first.

## Deploy and connect

Start the server and a compatible TCP output sink first. For the forwarding
example above, use the demo sink from the [main README](../README.md). Then:

```bash
go run ./cmd/deploy -name my-function -dir ./examples/my-function \
  -env BINARY_MODE=true,OUTPUT_HOST=host.docker.internal,OUTPUT_PORT=9100
```

Start the demo's frame source in another terminal, then attach the function:

```bash
make connect FUNCTION_NAME=my-function
```

Deployment builds and creates the container. The connection request starts
it. Redeploy after changing the function. `-env` accepts comma-separated
`KEY=value` pairs. Use a reachable host IP instead of `host.docker.internal`
on Linux, as described in the main README.

## Processing frames and chaining functions

TCP reads do not preserve frame boundaries. When transforming JPEG frames,
read the complete length-prefixed header and payload and preserve frame IDs
and source timestamps. See the [protocol](../README.md#workload-and-frame-protocol)
and [segmentation function](segmentation-demo/fn.go) for a complete example.
`NS` on timestamp fields means Unix nanoseconds. Set `BINARY_MODE=true` for
binary streams so the runtime does not send text acknowledgements upstream.

To send output to another deployed function, configure
`OUTPUT_HANDLER_HOST`, `OUTPUT_HANDLER_PORT`, and `OUTPUT_HANDLER_NAME`
instead of `OUTPUT_HOST`/`OUTPUT_PORT`. For multiple downstream functions,
use `OUTPUT_HANDLER_NAMES` and `SendOutputBytesTo(name, payload)`.

Included examples:

- [uppercase](uppercase/fn.go): text conversion using `fn` directly.
- [colorfilter](colorfilter/fn.go) and [watermark](watermark/fn.go): a sepia
  filter followed by a bottom-right PNG overlay from `watermark/3S.png`. See the
  [two-function demonstration](../README.md#two-function-demonstration) for
  deployment and connection commands. Each stage retains the frame ID and
  source timestamp and records its own processing timings. The final frame's
  processing timings describe the watermark stage.
- [segmentation-demo](segmentation-demo/fn.go): JPEG-to-mask processing with
  configurable benchmark refinement passes.
- [frametilesplitter](frametilesplitter/fn.go): splits frames and routes tiles
  to named workers.
- [frametilefuser](frametilefuser/fn.go): waits for each worker's tiles and
  assembles frames in order.

Function files reference symbols supplied by the runtime, so they do not
compile standalone unless tests provide those symbols. Deployment compiles
the function and runtime together.
