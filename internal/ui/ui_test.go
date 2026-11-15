package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/fr33dman/yandex-station-picklock/pkg/execution"
)

// testModel создаёт модель с двумя сценариями и фиксированным размером терминала.
func testModel() *model {
	m := newModel([]Entry{{Name: "first", Description: "Первый сценарий"}, {Name: "second", Description: "Второй сценарий"}}) // Создаём состояние экрана выбора.
	m.width, m.height = 100, 36
	return m
}

// TestSelectionRequiresExplicitConfirmation проверяет явное согласие перед запуском.
func TestSelectionRequiresExplicitConfirmation(t *testing.T) {
	m := testModel()               // Создаём модель UI с тестовым каталогом.
	m.handleKey(key{name: "down"}) // Обрабатываем клавишу и получаем действие приложения.
	if m.selection != 1 {
		t.Fatal("selection did not move") // Немедленно завершаем тест при нарушении условия.
	}
	if action := m.handleKey(key{name: "enter"}); action != noAction || m.phase != confirmPhase { // Обрабатываем клавишу и получаем действие приложения.
		t.Fatal("selection started the scenario without confirmation") // Немедленно завершаем тест при нарушении условия.
	}
	if action := m.handleKey(key{name: "enter"}); action != noAction || m.phase != choosePhase { // Обрабатываем клавишу и получаем действие приложения.
		t.Fatal("the default confirmation was not No") // Немедленно завершаем тест при нарушении условия.
	}
	m.handleKey(key{name: "enter"})                                                                            // Обрабатываем клавишу и получаем действие приложения.
	if action := m.handleKey(key{name: "paste", text: "y\n"}); action != noAction || m.phase != confirmPhase { // Обрабатываем клавишу и получаем действие приложения.
		t.Fatal("pasted text confirmed a scenario") // Немедленно завершаем тест при нарушении условия.
	}
	if action := m.handleKey(key{name: "text", text: "y"}); action != startAction || m.phase != runningPhase { // Обрабатываем клавишу и получаем действие приложения.
		t.Fatal("explicit Yes did not start the selected scenario") // Немедленно завершаем тест при нарушении условия.
	}
	if m.selection != 1 {
		t.Fatal("confirmed the wrong scenario") // Немедленно завершаем тест при нарушении условия.
	}
}

// TestPanelsCoverTheWholeAreaWithoutOverlaps проверяет заполнение области панелями без пересечений.
func TestPanelsCoverTheWholeAreaWithoutOverlaps(t *testing.T) {
	for _, width := range []int{51, 80, 120} {
		for count := 1; count <= 4; count++ { // Проверяем все варианты от одной до четырёх панелей.
			area := rect{x: 2, y: 3, w: width, h: 25}  // Область текущего диалога.
			seen := make(map[[2]int]bool)              // Создаём множество для проверки повторных элементов.
			for _, r := range taskRects(area, count) { // Делим доступную область между видимыми задачами.
				for y := r.y; y < r.y+r.h; y++ { // Вертикальная координата следующей строки.
					for x := r.x; x < r.x+r.w; x++ { // Горизонтальная координата следующего фрагмента.
						point := [2]int{x, y} // Координата ячейки для поиска перекрывающихся панелей.
						if seen[point] {
							t.Fatalf("overlapping tasks: %d/%d", width, count) // Завершаем тест с диагностикой полученных значений.
						}
						seen[point] = true
						if x < area.x || x >= area.x+area.w || y < area.y || y >= area.y+area.h {
							t.Fatal("task escaped its area") // Немедленно завершаем тест при нарушении условия.
						}
					}
				}
			}
			if len(seen) != area.w*area.h { // Проверяем количество доступных элементов.
				t.Fatalf("unused task area: %d/%d", width, count) // Завершаем тест с диагностикой полученных значений.
			}
		}
	}
}

// TestInputHandlesUTF8AndBracketedPaste проверяет посимвольный UTF-8, вставку и отдельный Escape.
func TestInputHandlesUTF8AndBracketedPaste(t *testing.T) {
	var input decoder                                    // Ожидание завершения фонового чтения ввода.
	var received []key                                   // Клавиши, полученные за текущий проход цикла.
	data := []byte("я\x1b[A\x1b[200~secret\ny\x1b[201~") // Представляем данные как байты.
	for _, b := range data {
		received = append(received, input.feed([]byte{b})...) // Добавляем распознанную клавишу или вставку.
	}
	if len(received) != 3 || received[0].text != "я" || received[1].name != "up" || received[2].name != "paste" || received[2].text != "secret y" { // Проверяем количество доступных элементов.
		t.Fatalf("decoded input: %+v", received) // Завершаем тест с диагностикой полученных значений.
	}
	if keys := input.feed([]byte{27}); len(keys) != 0 || !input.escapePending() { // Передаём фрагмент ввода декодеру.
		t.Fatal("Escape was not deferred to distinguish arrow keys") // Немедленно завершаем тест при нарушении условия.
	}
	if keys := input.flushEscape(); len(keys) != 1 || keys[0].name != "escape" { // Завершаем ожидание отдельного Escape.
		t.Fatal("standalone Escape was not delivered") // Немедленно завершаем тест при нарушении условия.
	}
}

// TestSecretInputIsEditableButNeverRendered проверяет редактирование и маскировку пароля.
func TestSecretInputIsEditableButNeverRendered(t *testing.T) {
	m := testModel()                                                                            // Создаём модель UI с тестовым каталогом.
	m.begin()                                                                                   // Подготавливаем модель к новому запуску.
	r := newRequest(t.Context(), askRequest, execution.Question{Text: "Пароль", Secret: true})  // Создаём запрос с метаданными текущей задачи.
	m.add(r)                                                                                    // Добавляем запрос задачи в диалоги UI.
	m.handleKey(key{name: "paste", text: "секрет123"})                                          // Обрабатываем клавишу и получаем действие приложения.
	m.handleKey(key{name: "left"})                                                              // Обрабатываем клавишу и получаем действие приложения.
	m.handleKey(key{name: "backspace"})                                                         // Обрабатываем клавишу и получаем действие приложения.
	if view := m.view(); strings.Contains(view, "секрет") || !strings.Contains(view, "*****") { // Собираем кадр текущего состояния UI.
		t.Fatal("password leaked into the screen") // Немедленно завершаем тест при нарушении условия.
	}
	m.handleKey(key{name: "enter"}) // Обрабатываем клавишу и получаем действие приложения.
	result := <-r.reply             // Ответ, отправленный завершённым диалогом.
	if result.value != "секрет13" {
		t.Fatalf("Unicode edit changed the submitted value: %q", result.value) // Завершаем тест с диагностикой полученных значений.
	}
	if len(m.dialogs) != 0 { // Проверяем количество доступных элементов.
		t.Fatal("answered input remained visible") // Немедленно завершаем тест при нарушении условия.
	}
}

// TestParallelQuestionsKeepTheirTaskIdentity проверяет маршрутизацию ответов параллельным задачам.
func TestParallelQuestionsKeepTheirTaskIdentity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) { // Проверяем конкурентный код с управляемым временем.
		app := New()                                   // Создаём приложение UI для проверки взаимодействия.
		app.active.Store(true)                         // Включаем приём событий UI без открытия терминала.
		defer close(app.done)                          // Разблокируем отправителей до ожидания их завершения.
		ctx, cancel := context.WithCancel(t.Context()) // Создаём контекст с отдельной функцией отмены.
		defer cancel()                                 // Отменяем дочерний контекст и освобождаем ресурсы.
		var answers sync.Map                           // Потокобезопасные ответы по идентификаторам задач.
		jobs := make([]execution.Job, 0, 2)            // Выделяем буфер нужной длины.
		for _, name := range []string{"one", "two"} {
			jobs = append(jobs, execution.NewSingleJob(execution.Stage{Name: name, Description: "description " + name, Executive: func(ctx context.Context) error { // Тестовый шаг имитирует нужный вариант выполнения.
				task, ok := execution.CurrentTask(ctx) // Извлекаем метаданные текущей задачи из контекста.
				if !ok {
					return errors.New("missing task context") // Отсутствие метаданных нарушает маршрутизацию ответов.
				}
				value, err := app.Ask(ctx, execution.Question{Text: "Question " + name, Default: name}) // Передаём вопрос выполняемой задачи в UI.
				if err == nil {
					answers.Store(task.ID, value) // Сохраняем ответ под идентификатором нужной задачи.
				}
				return err
			}}, nil))
		}
		pipeline, err := execution.NewPipeline(execution.NewParallelJobs(jobs, nil)) // Проверяем и собираем тестовое дерево задач.
		if err != nil {
			t.Fatal(err) // Немедленно завершаем тест при нарушении условия.
		}
		m := testModel()                                                                                 // Создаём модель UI с тестовым каталогом.
		m.begin()                                                                                        // Подготавливаем модель к новому запуску.
		done := app.start(ctx, Entry{Build: func(context.Context, execution.Interaction) (*Run, error) { // Фабрика возвращает заранее подготовленный тестовый запуск.
			return &Run{Pipeline: pipeline, Execute: pipeline.Execute}, nil
		}}, m.generation)
		for len(m.dialogs) < 2 { // Проверяем количество доступных элементов.
			switch event := (<-app.events).(type) { // Конкретный вид события из общей очереди.
			case readyEvent:
				m.run = event.run
				m.poll() // Читаем свежий снимок pipeline.
			case *request:
				m.add(event) // Добавляем запрос задачи в диалоги UI.
			}
		}
		first, second := m.dialogs[0].request, m.dialogs[1].request // Запросы двух параллельных задач.
		if first.task.ID == second.task.ID || first.task.ID == "" {
			t.Fatal("parallel tasks share an identity") // Немедленно завершаем тест при нарушении условия.
		}
		m.handleKey(key{name: "tab"})            // Обрабатываем клавишу и получаем действие приложения.
		if m.currentDialog().request != second { // Находим вопрос, которому принадлежит фокус ввода.
			t.Fatal("Tab did not focus the other task's question") // Немедленно завершаем тест при нарушении условия.
		}
		m.handleKey(key{name: "enter"}) // Обрабатываем клавишу и получаем действие приложения.
		m.handleKey(key{name: "enter"}) // Обрабатываем клавишу и получаем действие приложения.
		result := <-done                // Итог фонового сценария после отправки ответов.
		if result.err != nil {
			t.Fatal(result.err) // Немедленно завершаем тест при нарушении условия.
		}
		for _, r := range []*request{first, second} {
			value, ok := answers.Load(r.task.ID) // Проверяем ответ конкретной параллельной задачи.
			if !ok || value != r.task.Name {
				t.Fatalf("answer was routed to the wrong task: %v", value) // Завершаем тест с диагностикой полученных значений.
			}
		}
	})
}

// TestCancellationWaitsForCriticalWork проверяет ожидание начатой критической операции после отмены.
func TestCancellationWaitsForCriticalWork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) { // Проверяем конкурентный код с управляемым временем.
		app := New()                                                                                                                                 // Создаём приложение UI для проверки взаимодействия.
		app.active.Store(true)                                                                                                                       // Включаем приём событий UI без открытия терминала.
		defer close(app.done)                                                                                                                        // Разблокируем отправителей до ожидания их завершения.
		ctx, cancel := context.WithCancel(t.Context())                                                                                               // Создаём контекст с отдельной функцией отмены.
		defer cancel()                                                                                                                               // Отменяем дочерний контекст и освобождаем ресурсы.
		release := make(chan struct{})                                                                                                               // Барьер удерживает шаг до явного разрешения теста.
		pipeline, err := execution.NewPipeline(execution.NewSingleJob(execution.Stage{Name: "critical", Executive: func(ctx context.Context) error { // Тестовый шаг имитирует нужный вариант выполнения.
			<-ctx.Done() // Ожидаем отмены контекста.
			<-release
			return ctx.Err() // Проверяем отмену или истечение срока контекста.
		}}, nil))
		if err != nil {
			t.Fatal(err) // Немедленно завершаем тест при нарушении условия.
		}
		m := testModel()                                                                                 // Создаём модель UI с тестовым каталогом.
		m.begin()                                                                                        // Подготавливаем модель к новому запуску.
		done := app.start(ctx, Entry{Build: func(context.Context, execution.Interaction) (*Run, error) { // Фабрика возвращает заранее подготовленный тестовый запуск.
			return &Run{Pipeline: pipeline, Execute: pipeline.Execute}, nil
		}}, m.generation)
		ready := (<-app.events).(readyEvent) // Событие завершения сборки тестового сценария.
		m.run = ready.run
		synctest.Wait()                                                       // Ждём блокировки фоновых горутин перед проверкой состояния.
		if action := m.handleKey(key{name: "escape"}); action != stopAction { // Обрабатываем клавишу и получаем действие приложения.
			t.Fatal("Escape exited before cleanup") // Немедленно завершаем тест при нарушении условия.
		}
		cancel()        // Отменяем дочерний контекст и освобождаем ресурсы.
		synctest.Wait() // Ждём блокировки фоновых горутин перед проверкой состояния.
		select {
		case <-done:
			t.Fatal("UI runner completed before critical work") // Немедленно завершаем тест при нарушении условия.
		default:
		}
		if m.phase != runningPhase || !m.stopping {
			t.Fatal("UI did not retain the stopping screen") // Немедленно завершаем тест при нарушении условия.
		}
		close(release)                                                                     // Снимаем тестовый барьер завершения шага.
		m.finish(<-done)                                                                   // Сохраняем результат и показываем экран завершения.
		if m.stopping || m.phase != finishedPhase || !errors.Is(m.err, context.Canceled) { // Проверяем причину с учётом обёрток ошибки.
			t.Fatal("cancellation did not reach a terminal state") // Немедленно завершаем тест при нарушении условия.
		}
	})
}

// TestAudioOwnsItsBufferAcrossCancellation проверяет независимость копии WAV от буфера вызывающего кода.
func TestAudioOwnsItsBufferAcrossCancellation(t *testing.T) {
	app := New()                                   // Создаём приложение UI для проверки взаимодействия.
	app.active.Store(true)                         // Включаем приём событий UI без открытия терминала.
	defer close(app.done)                          // Разблокируем отправителей до ожидания их завершения.
	ctx, cancel := context.WithCancel(t.Context()) // Создаём контекст с отдельной функцией отмены.
	defer cancel()                                 // Отменяем дочерний контекст и освобождаем ресурсы.
	wav := []byte("private wave data")             // Представляем данные как байты.
	result := make(chan error, 1)                  // Буфер одного результата не задерживает завершение отправителя.
	go func() {                                    // В горутине выполняем проверяемое действие.
		result <- app.Play(ctx, wav) // Запрашиваем воспроизведение тестового WAV.
	}()
	r := (<-app.events).(*request)                          // Запрос, которому принадлежит текущий диалог.
	cancel()                                                // Отменяем дочерний контекст и освобождаем ресурсы.
	if err := <-result; !errors.Is(err, context.Canceled) { // Проверяем причину с учётом обёрток ошибки.
		t.Fatal(err) // Немедленно завершаем тест при нарушении условия.
	}
	clear(wav)                                // Затираем WAV после использования или отмены.
	if string(r.wav) != "private wave data" { // Преобразуем символы или байты в строку.
		t.Fatal("audio process retained the caller's buffer") // Немедленно завершаем тест при нарушении условия.
	}
	m := testModel() // Создаём модель UI с тестовым каталогом.
	m.begin()        // Подготавливаем модель к новому запуску.
	m.add(r)         // Добавляем запрос задачи в диалоги UI.
	for _, b := range r.wav {
		if b != 0 {
			t.Fatal("canceled audio copy was not cleared") // Немедленно завершаем тест при нарушении условия.
		}
	}
}

// TestTerminalTextCannotInjectEscapeSequences проверяет защиту кадра от управляющих последовательностей.
func TestTerminalTextCannotInjectEscapeSequences(t *testing.T) {
	m := testModel() // Создаём модель UI с тестовым каталогом.
	m.entries[0].Name = "safe\x1b]52;c;payload\x07\x1b[2J"
	view := m.view()                                                            // Собираем кадр текущего состояния UI.
	if strings.Contains(view, "\x1b]52") || strings.Contains(view, "\x1b[2J") { // Проверяем наличие ожидаемого фрагмента текста.
		t.Fatal("untrusted text escaped the canvas") // Немедленно завершаем тест при нарушении условия.
	}
	for _, dimensions := range [][2]int{{10, 5}, {50, 16}, {79, 24}, {120, 40}, {200, 60}} {
		m.width, m.height = dimensions[0], dimensions[1]
		if view := m.view(); strings.Count(view, "\r\n") != m.height-1 { // Собираем кадр текущего состояния UI.
			t.Fatalf("frame escaped terminal height at %v", dimensions) // Завершаем тест с диагностикой полученных значений.
		}
	}
}

// TestGraphIncludesNestedJobsAndFallback проверяет вложенные задачи и запасные ветви графа.
func TestGraphIncludesNestedJobsAndFallback(t *testing.T) {
	jobs := []execution.JobSnapshot{{ID: "1", Status: execution.Running, Jobs: []execution.JobSnapshot{{ID: "2", Status: execution.Failed, Stage: &execution.StageSnapshot{Name: "download", Status: execution.Failed}, Fallback: &execution.JobSnapshot{ID: "3", Status: execution.Running, Stage: &execution.StageSnapshot{Name: "retry", Status: execution.Running}}}}}} // Вложенный граф с ошибкой шага и работающей запасной ветвью.
	var text strings.Builder                                                                                                                                                                                                                                                                                                                                                // Видимый текст графа без разделения на цветовые фрагменты.
	for _, line := range graph(jobs, 80) {                                                                                                                                                                                                                                                                                                                                  // Строим строки графа из снимков задач.
		for _, part := range line.parts {
			text.WriteString(part.text) // Собираем видимый текст фрагментов графа.
		}
	}
	for _, want := range []string{"parallel", "FAIL", "download", "fallback", "RUN", "retry"} {
		if !strings.Contains(text.String(), want) { // Проверяем наличие ожидаемого фрагмента текста.
			t.Fatalf("graph lost %s: %s", want, text.String()) // Завершаем тест с диагностикой полученных значений.
		}
	}
}

// TestCancellationDoesNotHideCriticalFailure проверяет показ реального сбоя вместе с отменой.
func TestCancellationDoesNotHideCriticalFailure(t *testing.T) {
	canceled := fmt.Errorf("stage stopped: %w", context.Canceled) // Добавляем пояснение к возвращаемой ошибке.
	if !execution.IsCancellation(canceled) {                      // Отличаем чистую отмену от реального сбоя.
		t.Fatal("wrapped cancellation was not recognized") // Немедленно завершаем тест при нарушении условия.
	}
	unsafe := errors.Join(canceled, errors.New("boot readback differs; DO NOT REBOOT")) // Объединяем ошибки, сохраняя каждую причину.
	if execution.IsCancellation(unsafe) {                                               // Отличаем чистую отмену от реального сбоя.
		t.Fatal("a failed critical operation was treated as a clean cancellation") // Немедленно завершаем тест при нарушении условия.
	}
	m := testModel()                                                                                    // Создаём модель UI с тестовым каталогом.
	m.begin()                                                                                           // Подготавливаем модель к новому запуску.
	m.finish(runResult{err: unsafe})                                                                    // Сохраняем результат и показываем экран завершения.
	if m.outcome() != "Сценарий завершился с ошибкой" || !strings.Contains(m.view(), "DO NOT REBOOT") { // Получаем понятное пользователю состояние сценария.
		t.Fatal("critical failure is missing from the result screen") // Немедленно завершаем тест при нарушении условия.
	}
}

// TestOtherTaskCompletionPreservesInputFocus проверяет сохранение фокуса после закрытия чужого вопроса.
func TestOtherTaskCompletionPreservesInputFocus(t *testing.T) {
	m := testModel() // Создаём модель UI с тестовым каталогом.
	m.begin()        // Подготавливаем модель к новому запуску.
	for _, name := range []string{"one", "two", "three", "four"} {
		m.add(newRequest(t.Context(), askRequest, execution.Question{Text: name})) // Добавляем запрос задачи в диалоги UI.
	}
	m.focus = 2
	focused := m.currentDialog()              // Находим вопрос, которому принадлежит фокус ввода.
	m.resolve(m.dialogs[0].request, answer{}) // Закрываем диалог и возвращаем ответ его задаче.
	if m.currentDialog() != focused {         // Находим вопрос, которому принадлежит фокус ввода.
		t.Fatal("completion of another task redirected the input focus") // Немедленно завершаем тест при нарушении условия.
	}
	m.handleKey(key{name: "text", text: "answer"}) // Обрабатываем клавишу и получаем действие приложения.
	if string(focused.text) != "answer" {          // Преобразуем символы или байты в строку.
		t.Fatal("typed text reached the wrong task") // Немедленно завершаем тест при нарушении условия.
	}
}

// TestFourTaskPanelsShowTheirNamesAndDescriptions проверяет видимость описаний всех четырёх задач.
func TestFourTaskPanelsShowTheirNamesAndDescriptions(t *testing.T) {
	m := testModel() // Создаём модель UI с тестовым каталогом.
	m.begin()        // Подготавливаем модель к новому запуску.
	m.width, m.height = 120, 40
	for i := 0; i < 4; i++ { // Обходим все четыре панели тестового экрана.
		name := fmt.Sprintf("task-%d", i)                                                                                                                                                                                 // Форматируем текст для отображения.
		m.snapshot.Jobs = append(m.snapshot.Jobs, execution.JobSnapshot{ID: name, Status: execution.Running, Stage: &execution.StageSnapshot{Name: name, Description: "description " + name, Status: execution.Running}}) // Копируем список, не разделяя его массив с вызывающим кодом.
	}
	view := m.view()         // Собираем кадр текущего состояния UI.
	for i := 0; i < 4; i++ { // Обходим все четыре панели тестового экрана.
		if !strings.Contains(view, fmt.Sprintf("description task-%d", i)) { // Проверяем наличие ожидаемого фрагмента текста.
			t.Fatalf("task %d was lost from the screen", i) // Завершаем тест с диагностикой полученных значений.
		}
	}
}
