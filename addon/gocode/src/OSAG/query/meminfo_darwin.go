//+build darwin

package main

const GB = 1204 * 1024 * 1024

func getPhysMem() (float64, error) {
	// TODO: proper way to extract the available physical memor y
	// Defaulting to 4 GB to say on the safe side
	var physMem = float64(4 * GB)

	return physMem, nil
}