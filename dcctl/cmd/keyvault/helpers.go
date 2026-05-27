package keyvault

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

func formatTime(t time.Time) string {
	return t.Format("2006-01-02T15:04:05Z07:00")
}

func row(labelWidth int, label, value string) {
	if value == "" {
		return
	}
	fmt.Printf("  %-*s %s\n", labelWidth, label+":", value)
}

func confirm(prompt string, force bool) bool {
	if force {
		return true
	}
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y")
}
