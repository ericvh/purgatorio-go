//go:build wasip1

package main

// growMemory is wasm's memory.grow: the old size in pages, or -1.
func growMemory(pages int32) int32
