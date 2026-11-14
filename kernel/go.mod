module github.com/ericvh/purgatorio-go/kernel

go 1.27.2

require (
	github.com/ericvh/purgatorio-go v0.0.0-00010101000000-000000000000
	github.com/usbarmory/go-net v0.0.0-20261027112401-7efaf89ccad1
	github.com/usbarmory/tamago v1.27.4
	gvisor.dev/gvisor v0.0.0-20250911055229-61a46406f068
)

require (
	github.com/google/btree v1.1.2 // indirect
	github.com/soypat/lneto v0.3.2 // indirect
	github.com/tetratelabs/wazero v1.12.0 // indirect
	golang.org/x/sys v0.44.0 // indirect
	golang.org/x/time v0.7.0 // indirect
)

replace github.com/ericvh/purgatorio-go => ../
