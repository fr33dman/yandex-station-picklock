package execution

import (
	"context"
	"errors"
	"sync"
)

// Status задаёт состояние шага, задачи или pipeline.
type Status string

const (
	Pending   Status = "pending"   // Узел ожидает запуска.
	Running   Status = "running"   // Действие узла выполняется.
	Succeeded Status = "succeeded" // Узел успешно завершён, возможно после восстановления.
	Failed    Status = "failed"    // Узел завершился с ошибкой.
	Canceled  Status = "canceled"  // Выполнение прервано отменой контекста.
	Skipped   Status = "skipped"   // Узел не запускался из-за результата предыдущих действий.
)

var ErrAlreadyStarted = errors.New("execution already started; create a new pipeline for another run") // Повторное выполнение требует нового дерева задач.

// IsCancellation распознаёт только чистую отмену, включая обёртки.
// Реальная ошибка, объединённая с отменой, должна остаться видимой пользователю.
func IsCancellation(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok { // Ошибка с несколькими причинами и признак такого типа.
		causes := joined.Unwrap() // Получаем все объединённые причины ошибки.
		if len(causes) == 0 {     // Пустой список причин не подтверждает отмену.
			return false
		}
		for _, cause := range causes {
			if !IsCancellation(cause) { // Проверяем, содержит ли причина только отмену.
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok { // Обёртка одной причины и признак такого типа.
		return IsCancellation(wrapped.Unwrap()) // Проверяем, содержит ли причина только отмену.
	}
	return err == context.Canceled
}

// StageSnapshot содержит описание шага и результат его последнего выполнения.
type StageSnapshot struct {
	Name        string `json:"name"`            // Название исходного шага.
	Description string `json:"description"`     // Описание действий шага.
	Status      Status `json:"status"`          // Результат самого шага, независимо от восстановления.
	Error       string `json:"error,omitempty"` // Текст ошибки шага, если она есть.
	IsCritical  bool   `json:"isCritical"`      // Сбой шага останавливает весь pipeline.
	IsOptional  bool   `json:"isOptional"`      // Пользователь мог пропустить этот шаг.
}

// JobSnapshot содержит состояние задачи, её дочерних узлов и запасного пути.
type JobSnapshot struct {
	ID       string         `json:"id"`                 // Идентификатор узла в pipeline.
	Status   Status         `json:"status"`             // Состояние задачи с учётом восстановления.
	Error    string         `json:"error,omitempty"`    // Итоговая ошибка задачи.
	Stage    *StageSnapshot `json:"stage,omitempty"`    // Снимок исходного шага одиночной задачи.
	Jobs     []JobSnapshot  `json:"jobs,omitempty"`     // Снимки ветвей параллельной группы.
	Fallback *JobSnapshot   `json:"fallback,omitempty"` // Снимок запасного пути, если он задан.
}

// PipelineSnapshot содержит общий результат и дерево состояний задач.
type PipelineSnapshot struct {
	Status   Status        `json:"status"`             // Общее состояние исполнения.
	Error    string        `json:"error,omitempty"`    // Текст итоговой ошибки.
	Warnings []string      `json:"warnings,omitempty"` // Сбои шагов, которые не остановили pipeline.
	Jobs     []JobSnapshot `json:"jobs"`               // Корневые задачи со всеми вложенными состояниями.
}

// warnings собирает сбои best-effort шагов, которые не остановили pipeline.
func warnings(jobs []JobSnapshot) []string {
	var result []string            // Тексты допущенных сбоев в порядке дерева задач.
	var walk func(JobSnapshot)     // Рекурсивный обход задач, ветвей и запасных путей.
	walk = func(job JobSnapshot) { // Собираем сбои текущего узла и его потомков.
		if job.Stage != nil && job.Status == Failed && !job.Stage.IsCritical {
			text := job.Stage.Error // Текст сбоя шага, который не остановил pipeline.
			if text == "" {
				text = "шаг завершился с ошибкой" // Поясняем сбой без текста ошибки.
			}
			result = append(result, job.Stage.Name+": "+text) // Сохраняем сбой вместе с именем шага.
		}
		for _, child := range job.Jobs {
			walk(child) // Обходим дочерний узел и его запасную ветвь.
		}
		if job.Fallback != nil {
			walk(*job.Fallback) // Обходим дочерний узел и его запасную ветвь.
		}
	}
	for _, job := range jobs {
		walk(job) // Обходим корневой узел и его потомков.
	}
	return result
}

// runState защищает состояние узла и его принадлежность pipeline от конкурентного доступа.
type runState struct {
	mu     sync.RWMutex // Защита полей состояния и принадлежности узла.
	status Status       // Текущее состояние исполнения.
	err    error        // Исходная ошибка для построения снимка.
	owned  bool         // Узел уже закреплён за одним pipeline.
	id     string       // Идентификатор, назначенный владельцем.
}

// claim закрепляет свободный узел за pipeline и присваивает ему идентификатор.
func (s *runState) claim(id string) bool {
	s.mu.Lock()         // Блокируем изменение состояния узла.
	defer s.mu.Unlock() // Освобождаем блокировку состояния узла.
	if s.owned {
		return false
	}
	s.owned = true
	s.id = id
	return true
}

// release снимает владение после неудачной сборки pipeline.
func (s *runState) release() {
	s.mu.Lock()         // Блокируем изменение состояния узла.
	defer s.mu.Unlock() // Освобождаем блокировку состояния узла.
	s.owned = false
	s.id = ""
}

// start переводит ожидающий узел в работу и запрещает повторный запуск.
func (s *runState) start() error {
	s.mu.Lock()         // Блокируем изменение состояния узла.
	defer s.mu.Unlock() // Освобождаем блокировку состояния узла.
	if s.status != Pending {
		return ErrAlreadyStarted
	}
	s.status = Running
	return nil
}

// finish записывает итоговый статус и ошибку узла.
func (s *runState) finish(ctx context.Context, err error) {
	s.mu.Lock()                                   // Блокируем изменение состояния узла.
	defer s.mu.Unlock()                           // Освобождаем блокировку состояния узла.
	s.status, s.err = resultStatus(ctx, err), err // Выбираем итоговый статус по ошибке и контексту.
}

// skip пропускает только ещё не начатый узел.
func (s *runState) skip() {
	s.mu.Lock()         // Блокируем изменение состояния узла.
	defer s.mu.Unlock() // Освобождаем блокировку состояния узла.
	if s.status == Pending {
		s.status = Skipped
	}
}

// snapshot под блокировкой чтения возвращает статус, ошибку и идентификатор.
func (s *runState) snapshot() (Status, string, string) {
	s.mu.RLock()                            // Защищаем чтение состояния узла.
	defer s.mu.RUnlock()                    // Снимаем блокировку чтения узла.
	return s.status, errorText(s.err), s.id // Возвращаем согласованное состояние под блокировкой чтения.
}

// resultStatus выбирает итоговый статус; отменённый контекст имеет приоритет.
func resultStatus(ctx context.Context, err error) Status {
	if ctx.Err() != nil { // Проверяем отмену или истечение срока контекста.
		return Canceled
	}
	if err != nil {
		return Failed
	}
	return Succeeded
}

// withCancellation добавляет причину отмены, сохраняя исходную ошибку.
func withCancellation(ctx context.Context, err error) error {
	if cause := ctx.Err(); cause != nil && !errors.Is(err, cause) { // Добавляем отмену только если её ещё нет среди причин.
		return errors.Join(err, cause) // Объединяем ошибки, сохраняя каждую причину.
	}
	return err
}

// errorText возвращает текст ошибки или пустую строку.
func errorText(err error) string {
	if err != nil {
		return err.Error() // Возвращаем текст сохранённой ошибки.
	}
	return ""
}

// cloneJobs глубоко копирует список снимков задач.
func cloneJobs(jobs []JobSnapshot) []JobSnapshot {
	cloned := make([]JobSnapshot, len(jobs)) // Выделяем независимый список снимков задач.
	for i, job := range jobs {
		cloned[i] = cloneJob(job) // Копируем узел вместе с вложенными данными.
	}
	return cloned
}

// cloneJob копирует шаг, запасной путь и все дочерние снимки задачи.
func cloneJob(job JobSnapshot) JobSnapshot {
	if job.Stage != nil {
		stage := *job.Stage // Отдельная копия снимка исходного шага.
		job.Stage = &stage
	}
	if job.Fallback != nil {
		fallback := cloneJob(*job.Fallback) // Копируем узел вместе с вложенными данными.
		job.Fallback = &fallback
	}
	if job.Jobs != nil {
		job.Jobs = cloneJobs(job.Jobs) // Глубоко копируем дочерние снимки.
	}
	return job
}
