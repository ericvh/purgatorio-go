//go:build !wasip1

package sys

// Host stubs for the M3 calls.

const (
	EXPWAIT  = 0
	EXPASYNC = 1

	WaitRead     = 0
	WaitWrite    = 1
	WaitChanRecv = 2
	WaitChanSend = 3
)

type Chan int

type AltOp struct {
	C      Chan
	Send   bool
	Data   []byte
	Caps   []Chan
	Got    []byte
	GotCap []Chan
	Closed bool
}

type FileIO struct{ Read, Write Chan }

type ReadReq struct {
	Off   int64
	Count int
	Fid   int
}

type WriteReq struct {
	Off  int64
	Data []byte
	Fid  int
}

func Mount(int, int, string, int, string) error { return errHost }
func Fauth(int, string) (int, error)            { return -1, errHost }
func Fversion(int, int, string) (string, error) { return "", errHost }
func Export(int, string, int) error             { return errHost }
func NewChan(int) Chan                          { return -1 }
func (Chan) Close() error                       { return errHost }
func (Chan) Send([]byte, ...Chan) error         { return errHost }
func (Chan) Recv(int) ([]byte, []Chan, error)   { return nil, nil, errHost }
func Alt([]AltOp, bool) (int, error)            { return -1, errHost }
func Wait([][2]int, int) (int, error)           { return -1, errHost }
func File2chan(string, string) (*FileIO, error) { return nil, errHost }
func (*ReadReq) Closed() bool                   { return true }
func (*WriteReq) Closed() bool                  { return true }
func (*ReadReq) Reply([]byte, string)           {}
func (*WriteReq) Reply(int, string)             {}
func (*FileIO) Next() (any, error)              { return nil, errHost }

func Detach() int     { return -1 }
func Close(int) error { return errHost }
func Detach() int     { return -1 }

type Module int

func Load(string) (Module, error)                  { return -1, errHost }
func (Module) Call(string, []byte) ([]byte, error) { return nil, errHost }
func (Module) Unload() error                       { return errHost }
