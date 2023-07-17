package formatter

func allTrue(args []bool) bool {
	for _, a := range args {
		if !a {
			return false
		}
	}

	return true
}

func sliceCountTrue(args []bool) int {
	i := 0

	for _, a := range args {
		if a {
			i++
		}
	}

	return i
}
