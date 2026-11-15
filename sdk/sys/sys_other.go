//go:build !wasip1

package sys

import "errors"

// Host stubs, so guest packages build, vet and test on the host. Guests
// only run under wasip1.

var errHost = errors.New("not in a guest")

func Errstr() string                                     { return "" }
func Werrstr(string)                                     {}
func Exits(s string)                                     { panic("sys.Exits outside a guest: " + s) }
func writeStderr(string)                                 {}
func Open(string, int) (int, error)                      { return -1, errHost }
func Create(string, int, uint32) (int, error)            { return -1, errHost }
func Remove(string) error                                { return errHost }
func Chdir(string) error                                 { return errHost }
func Stat(string) (*Dir, error)                          { return nil, errHost }
func Fstat(int) (*Dir, error)                            { return nil, errHost }
func Wstat(string, *Dir) error                           { return errHost }
func Fwstat(int, *Dir) error                             { return errHost }
func Dirread(int) ([]Dir, error)                         { return nil, errHost }
func Fd2path(int) (string, error)                        { return "", errHost }
func Dup(int, int) (int, error)                          { return -1, errHost }
func Pipe() ([2]int, error)                              { return [2]int{}, errHost }
func Iounit(int) int                                     { return 0 }
func Seek(int, int64, int) (int64, error)                { return -1, errHost }
func Bind(string, string, int) (int, error)              { return -1, errHost }
func Unmount(string, string) error                       { return errHost }
func Pctl(int, []int) (int, error)                       { return -1, errHost }
func Spawn(string, []string, int, [][2]int) (int, error) { return -1, errHost }
func Exec(string, []string) error                        { return errHost }
func Stream(int, int, int) int                           { return -1 }
func Sleep(int) error                                    { return errHost }
func Millisec() int                                      { return 0 }
