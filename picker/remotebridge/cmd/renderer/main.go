package main

import (
	"fmt"
	"net"
	"os"

	"github.com/noamsto/lazytmux/picker/remotebridge/render"
)

func main() {
	sock := os.Getenv("LZTMUX_RENDER_SOCK")
	pane := os.Getenv("LZTMUX_RENDER_PANE")
	conn, err := net.Dial("unix", sock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "renderer: dial %s: %v\r\n", sock, err)
		os.Exit(1)
	}
	defer conn.Close()
	if err := render.Run(conn, pane, os.Stdin, os.Stdout,
		func() (func() error, error) { return render.MakeRaw(0) },
		func(int, int) {}); err != nil {
		fmt.Fprintf(os.Stderr, "renderer: %v\r\n", err)
		os.Exit(1)
	}
}
