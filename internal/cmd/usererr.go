package cmd

type userError struct {
	msg        string
	detail     string
	suggestion string
	errMsg     string
	err        error
}

func (e *userError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	if e.errMsg != "" {
		return e.errMsg
	}
	return e.msg
}

func (e *userError) UserMessage() string { return e.msg }
func (e *userError) Detail() string      { return e.detail }
func (e *userError) Suggestion() string  { return e.suggestion }
func (e *userError) Unwrap() error       { return e.err }
