package canvas

const scrambleGlyphs = "#%&$@*+=<>/\\|?!0123456789"

// Scramble returns text with the letters not settled yet swapped for other
// glyphs. The letters settle from left to right as progress goes from 0 to 1,
// and tick changes the glyphs of the ones still to settle. Spaces are kept.
func Scramble(text string, progress float64, tick int) string {
	runes := []rune(text)
	settled := int(clamp(progress, 0, 1) * float64(len(runes)))
	for i := settled; i < len(runes); i++ {
		if runes[i] != ' ' {
			runes[i] = rune(scrambleGlyphs[mix(i, tick)%uint32(len(scrambleGlyphs))])
		}
	}
	return string(runes)
}

// mix spreads two numbers into one that looks random and is always the same
// for the same pair.
func mix(a, b int) uint32 {
	h := uint32(a)*2654435761 ^ uint32(b)*2246822519
	h ^= h >> 15
	h *= 2246822519
	h ^= h >> 13
	return h
}
