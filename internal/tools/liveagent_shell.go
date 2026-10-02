package tools

import "errors"

// Shell pipe inheritance must be detached before background execution.
func liveagentValidateBackground(command string) error {
	var quote byte
	escaped, stdout, stderr := false, false, false
	for i := 0; i < len(command); i++ {
		c := command[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quote = c
			continue
		}
		if c == '#' {
			for i+1 < len(command) && command[i+1] != '\n' {
				i++
			}
			continue
		}
		if c == ';' || c == '\n' {
			stdout = false
			stderr = false
			continue
		}
		if c == '&' {
			if i+1 < len(command) && command[i+1] == '&' {
				i++
				stdout = false
				stderr = false
				continue
			}
			if i+1 < len(command) && command[i+1] == '>' {
				i++
				stdout = true
				stderr = true
				continue
			}
			if i > 0 && command[i-1] == '>' {
				continue
			}
			if !stdout || !stderr {
				return errors.New("background Bash commands must detach stdout and stderr before using &: redirect both streams to a log file")
			}
			stdout = false
			stderr = false
		}
		if c == '|' {
			stdout = false
			stderr = false
			if i+1 < len(command) && command[i+1] == '|' {
				i++
			}
		}
		if c == '>' {
			if i > 0 && command[i-1] == '>' {
				continue
			}
			if i > 0 && command[i-1] == '2' {
				stderr = true
			} else {
				stdout = true
			}
		}
	}
	return nil
}
