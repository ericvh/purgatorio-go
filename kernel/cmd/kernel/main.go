// Command kernel is the Purgatorio-Go native kernel for QEMU's aarch64
// virt machine (Phase K). K1: boot, the console, the timer and two
// goroutines taking turns.
package main

import (
	"fmt"
	"time"

	"github.com/ericvh/purgatorio-go/kernel/board/virt"
)

func main() {
	fmt.Println("Purgatorio-Go kernel (aarch64, QEMU virt)")
	done := make(chan bool)
	turn := make(chan int)
	go func() {
		for i := range 3 {
			n := <-turn
			fmt.Printf("thread a: tick %d (%d)\n", i, n)
			time.Sleep(10 * time.Millisecond)
			turn <- n + 1
		}
		done <- true
	}()
	go func() {
		turn <- 0
		for i := range 3 {
			n := <-turn
			fmt.Printf("thread b: tick %d (%d)\n", i, n)
			if i < 2 {
				turn <- n + 1
			}
		}
		done <- true
	}()
	<-done
	<-done
	fmt.Println("k1 ok")
	virt.PowerOff()
}
