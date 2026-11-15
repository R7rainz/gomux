package sock

import "golang.org/x/sys/unix"

func peer(fd int) (uid uint32, pid int, err error) {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, 0, err
	}
	return cred.Uid, int(cred.Pid), nil
}
