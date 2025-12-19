package transcoding

func audioToAudio(input, output string, codec string, bitrate string) error {
	return transcode(
		input,
		output,
		"-c:a", codec,
		"-b:a", bitrate,
	)
}
