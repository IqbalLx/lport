package main

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
)

// App represents the main application
type App struct {
	model *Model
}

// NewApp creates a new application instance
func NewApp() *App {
	return &App{
		model: NewModel(),
	}
}

// Run starts the application
func (a *App) Run() error {
	// Create the Bubble Tea program
	p := tea.NewProgram(a.model)

	// Run the program
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("failed to run TUI application: %w", err)
	}

	if a.model.detached {
		running := a.model.RunningForwards()
		fmt.Printf("Detached. %d forward(s) still running:\n", len(running))
		for _, line := range running {
			fmt.Printf("  %s\n", line)
		}
		fmt.Println("Run lport again to manage them.")
	}

	return nil
}
