package observer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"golang.org/x/term"
)

type terminalAuthenticator struct {
	phone  string
	input  io.Reader
	output io.Writer
	reader *bufio.Reader
}

func newTerminalAuthenticator(phone string, input io.Reader, output io.Writer) *terminalAuthenticator {
	return &terminalAuthenticator{
		phone:  phone,
		input:  input,
		output: output,
		reader: bufio.NewReader(input),
	}
}

func (a *terminalAuthenticator) Phone(context.Context) (string, error) {
	return a.phone, nil
}

func (a *terminalAuthenticator) Code(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
	return a.readLine(ctx, "Telegram login code: ")
}

func (a *terminalAuthenticator) Password(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	input, ok := a.input.(*os.File)
	if !ok || !term.IsTerminal(int(input.Fd())) {
		return "", errors.New("telegram 2fa terminal unavailable: interactive input required")
	}
	if _, err := fmt.Fprint(a.output, "Telegram 2FA password: "); err != nil {
		return "", fmt.Errorf("password prompt write failed: %w", err)
	}
	password, err := term.ReadPassword(int(input.Fd()))
	if _, newlineErr := fmt.Fprintln(a.output); err == nil && newlineErr != nil {
		err = newlineErr
	}
	if err != nil {
		return "", fmt.Errorf("hidden password read failed: %w", err)
	}
	return strings.TrimSpace(string(password)), nil
}

func (*terminalAuthenticator) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("telegram account unavailable: configured phone number must belong to an existing account")
}

func (*terminalAuthenticator) AcceptTermsOfService(context.Context, tg.HelpTermsOfService) error {
	return errors.New("telegram account sign-up unsupported: existing account required")
}

func (a *terminalAuthenticator) readLine(ctx context.Context, prompt string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if _, err := fmt.Fprint(a.output, prompt); err != nil {
		return "", fmt.Errorf("prompt write failed: %w", err)
	}
	value, err := a.reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("input read failed: %w", err)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("input empty")
	}
	return value, nil
}
