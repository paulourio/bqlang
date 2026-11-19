package formatter

func allTrue(args []bool) bool {
	for _, a := range args {
		if !a {
			return false
		}
	}

	return true
}
