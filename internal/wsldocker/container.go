package wsldocker

import "errors"

func validateContainerID(containerID string) error {
	if len(containerID) != 64 {
		return errors.New("container ID must be a full 64-hex value")
	}
	for _, char := range containerID {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return errors.New("container ID must be lowercase hexadecimal")
		}
	}
	return nil
}
