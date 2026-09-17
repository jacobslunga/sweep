package main

import "golang.org/x/sys/unix"

func openDirectoryAt(fd int, name string) (int, error) {
	return unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
}
