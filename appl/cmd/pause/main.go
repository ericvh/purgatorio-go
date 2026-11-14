// Command pause does nothing, forever (Inferno pause(1)); kill ends it.
package main

import "github.com/ericvh/purgatorio-go/sdk/sys"

func main() {
	for sys.Sleep(1<<30) == nil {
	}
}
