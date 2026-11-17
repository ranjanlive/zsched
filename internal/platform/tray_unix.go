//go:build !windows

package platform

func Tray(items func() []MenuItem, action func(string), done <-chan struct{}) error {
	<-done
	return nil
}
func Balloon(title, body string) {}
