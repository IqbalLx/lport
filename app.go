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
	
	return nil
}