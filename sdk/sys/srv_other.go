//go:build !wasip1

package sys

func Iph2a(string) ([]string, error)       { return nil, errHost }
func Ipa2h(string) ([]string, error)       { return nil, errHost }
func Ipn2p(string, string) (string, error) { return "", errHost }
