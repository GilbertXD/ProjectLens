//go:build !darwin && !linux

package gitrepo

func InspectRootReadOnly(string) (readOnly bool, known bool, err error) {
	return false, false, nil
}
