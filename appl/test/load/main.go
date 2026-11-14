// Command load checks purgatorio.load and call with the testmod module.
package main

import (
	"fmt"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	m, err := sys.Load("/bin/lib/testmod.wasm")
	if err != nil {
		fmt.Println("load:", err)
		sys.Exits("fail:load")
	}
	out, err := m.Call("echo", []byte("hello"))
	fmt.Printf("echo %q %v\n", out, err)
	_, err = m.Call("fail", nil)
	fmt.Printf("fail %v\n", err)
	_, err = m.Call("nope", nil)
	fmt.Printf("nope %v\n", err)
	sn, _ := m.Call("sysname", nil)
	host, _ := os.ReadFile("/dev/sysname")
	fmt.Printf("same prog %v\n", string(sn) == string(host))
	big, err := m.Call("big", nil)
	fmt.Printf("big %d %v\n", len(big), err)
	m.Unload()
	if _, err := m.Call("echo", nil); err == nil {
		fmt.Println("call after unload worked")
	}
	if _, err := sys.Load("/bin/echo.wasm"); err != nil {
		fmt.Println("command:", err)
	}
}
