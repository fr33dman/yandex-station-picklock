package scenario_test

import (
	"context"
	"errors"
	"testing"

	"github.com/fr33dman/yandex-station-picklock/internal/scenario"
	"github.com/fr33dman/yandex-station-picklock/pkg/execution"
)

// TestScenarioRun проверяет запуск сценария, повторный запуск и отсутствие pipeline.
func TestScenarioRun(t *testing.T) {
	ran := 0 // Число фактически выполненных шагов.
	pipeline, err := execution.NewPipelineFromStages(execution.NewStage("one", "", func(context.Context) error {
		ran++
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	s := scenario.Scenario{Name: "test", Description: "one step", Pipeline: pipeline} // Сценарий, запускаемый через публичный метод Run.
	if err := s.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ran != 1 {
		t.Fatalf("steps: %d", ran) // Завершаем тест с диагностикой полученных значений.
	}
	if err := s.Run(t.Context()); !errors.Is(err, execution.ErrAlreadyStarted) {
		t.Fatalf("repeated run: %v", err) // Завершаем тест с диагностикой полученных значений.
	}
	if err := (scenario.Scenario{}).Run(t.Context()); err == nil {
		t.Fatal("accepted a missing scenario pipeline") // Немедленно завершаем тест при нарушении условия.
	}
}
