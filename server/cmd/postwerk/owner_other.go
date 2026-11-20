//go:build !unix

package main

func chownLike(root, ref string) {}
