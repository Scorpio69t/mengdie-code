package sum

func SumTo(limit int) int {
	total := 0
	for value := 0; value < limit; value++ {
		total += value
	}
	return total
}