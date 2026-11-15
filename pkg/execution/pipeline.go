package execution

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"
)

// Pipeline владеет деревом одного запуска и последним снимком его состояния.
type Pipeline struct {
	mu       sync.RWMutex     // Защита признака запуска и последнего снимка.
	started  bool             // Запрещает повторное выполнение дерева.
	jobs     []Job            // Корневые задачи в последовательном порядке.
	snapshot PipelineSnapshot // Последний снимок секундного опроса или границы задачи.
}

// NewPipeline проверяет дерево и закрепляет задачи за единственным запуском.
func NewPipeline(jobs ...Job) (*Pipeline, error) {
	states, err := validateJobs(jobs) // Проверяем дерево и собираем состояния всех узлов.
	if err != nil {
		return nil, err
	}

	for i, state := range states {
		if !state.claim(strconv.Itoa(i + 1)) { // Закрепляем узел за pipeline с уникальным идентификатором.
			for _, claimed := range states[:i] {
				claimed.release() // Освобождаем узлы при неудачной сборке pipeline.
			}
			return nil, errors.New("job already belongs to a pipeline") // Запрещаем разделять задачу между разными запусками.
		}
	}
	p := &Pipeline{jobs: append([]Job(nil), jobs...)} // Копируем список, не разделяя его массив с вызывающим кодом.
	p.capture(Pending, nil)                           // Обновляем общий снимок pipeline.
	return p, nil
}

// NewPipelineFromStages собирает последовательный pipeline из готовых шагов.
// Критичность и необязательность шага берутся из Stage; запасных путей нет.
func NewPipelineFromStages(stages ...Stage) (*Pipeline, error) {
	jobs := make([]Job, 0, len(stages))
	for _, stage := range stages {
		jobs = append(jobs, NewSingleJob(stage, nil)) // Каждый шаг становится отдельной задачей по порядку.
	}
	return NewPipeline(jobs...)
}

// Execute последовательно запускает корневые задачи в горутинах и опрашивает их раз в секунду.
// Сбой критичного шага пропускает оставшиеся; сбой best-effort шага остаётся только в снимке.
// При отмене ждёт начатую работу; шаги должны сами учитывать контекст.
func (p *Pipeline) Execute(ctx context.Context) error {
	p.mu.Lock() // Блокируем изменение состояния pipeline.
	if p.started {
		p.mu.Unlock() // Снимаем блокировку изменения pipeline.
		return ErrAlreadyStarted
	}
	p.started = true
	p.snapshot.Status = Running
	p.mu.Unlock() // Снимаем блокировку изменения pipeline.

	ticker := time.NewTicker(time.Second) // Создаём секундный таймер обновления состояния.
	defer ticker.Stop()                   // Останавливаем таймер опроса при выходе.
	var err error                         // Ошибка текущей операции.
	for i, job := range p.jobs {
		if err = ctx.Err(); err == nil { // Проверяем отмену или истечение срока контекста.
			result := make(chan error, 1) // Буфер одного результата не задерживает завершение отправителя.
			go func() {                   // Горутина выполняет задачу и передаёт её результат.
				result <- job.execute(ctx) // Выполняем дочернюю задачу с общим контекстом.
			}()
		waiting:
			for {
				select {
				case <-ticker.C:
					p.capture(Running, nil) // Обновляем общий снимок pipeline.
				case err = <-result:
					break waiting
				}
			}
		}
		if err == nil {
			p.capture(Running, nil) // Обновляем общий снимок pipeline.
			continue
		}
		if ctx.Err() != nil || job.critical() { // Отмена и критичный сбой останавливают цепочку.
			for _, remaining := range p.jobs[i:] {
				remaining.skip() // Пропускаем оставшиеся задачи после остановки.
			}
			break
		}
		// Best-effort шаг уже записал сбой в свой снимок: продолжаем следующие шаги.
		err = nil
		p.capture(Running, nil) // Обновляем общий снимок pipeline.
	}
	err = withCancellation(ctx, err)       // Сохраняем ошибку операции вместе с причиной отмены.
	p.capture(resultStatus(ctx, err), err) // Сохраняем итог сразу, без ожидания следующего тика.
	return err
}

// Snapshot возвращает глубокую копию последнего опроса для безопасного чтения UI.
func (p *Pipeline) Snapshot() PipelineSnapshot {
	p.mu.RLock()                             // Блокируем запись на время чтения снимка.
	defer p.mu.RUnlock()                     // Освобождаем блокировку чтения снимка.
	snapshot := p.snapshot                   // Отдельный снимок состояния для вызывающего кода.
	snapshot.Jobs = cloneJobs(snapshot.Jobs) // Глубоко копируем дочерние снимки.
	snapshot.Warnings = append([]string(nil), snapshot.Warnings...)
	return snapshot
}

// capture собирает состояния задач и атомарно заменяет снимок pipeline.
func (p *Pipeline) capture(status Status, err error) {
	jobs := make([]JobSnapshot, len(p.jobs)) // Выделяем независимый список снимков задач.
	for i, job := range p.jobs {
		jobs[i] = job.Snapshot() // Получаем снимок дочерней задачи.
	}
	p.mu.Lock()                                                                                                // Блокируем изменение состояния pipeline.
	p.snapshot = PipelineSnapshot{Status: status, Error: errorText(err), Jobs: jobs, Warnings: warnings(jobs)} // Атомарно сохраняем собранные статусы, ошибку и допущенные сбои.
	p.mu.Unlock()                                                                                              // Снимаем блокировку изменения pipeline.
}

// validateJobs обходит дерево и отвергает пустые, повторные и неподдерживаемые узлы.
func validateJobs(jobs []Job) ([]*runState, error) {
	seen := make(map[*runState]bool) // Создаём множество для проверки повторных элементов.
	var states []*runState           // Состояния узлов, найденных при проверке дерева.
	var visit func(Job) error        // Рекурсивный обработчик проверки дерева.
	visit = func(job Job) error {    // Проверяем узел, затем рекурсивно обходим его ветви.
		var state *runState        // Состояние текущего проверяемого узла.
		var children []Job         // Прямые дочерние задачи текущего узла.
		var fallback Job           // Запасной путь текущей задачи.
		switch job := job.(type) { // Конкретный тип проверяемого узла.
		case *SingleJob:
			if job == nil || job.stage.Name == "" || job.stage.Executive == nil {
				return errors.New("single job requires a named stage with an executive") // Одиночной задаче нужны имя шага и действие.
			}
			state, fallback = &job.state, job.fallback
		case *ParallelJobs:
			if job == nil || len(job.jobs) == 0 { // Параллельная группа должна содержать хотя бы одну задачу.
				return errors.New("parallel job requires children") // Отклоняем пустую параллельную группу.
			}
			state, children, fallback = &job.state, job.jobs, job.fallback
		default:
			return fmt.Errorf("unsupported job: %T", job) // Добавляем пояснение к возвращаемой ошибке.
		}
		if seen[state] {
			return errors.New("a job may occur only once in a pipeline") // Повторный узел создаёт неоднозначное дерево выполнения.
		}
		seen[state] = true
		states = append(states, state) // Запоминаем состояние проверенного узла.
		for _, child := range children {
			if err := visit(child); err != nil { // Рекурсивно проверяем следующий узел дерева.
				return err
			}
		}
		if fallback != nil {
			return visit(fallback) // Рекурсивно проверяем следующий узел дерева.
		}
		return nil
	}
	for _, job := range jobs {
		if err := visit(job); err != nil { // Рекурсивно проверяем следующий узел дерева.
			return nil, err
		}
	}
	return states, nil
}
