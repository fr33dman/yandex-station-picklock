// Package ui реализует полноэкранный CLI и порт взаимодействия со сценарием.
package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fr33dman/yandex-station-picklock/pkg/execution"
)

// Entry описывает сценарий каталога; Build вызывается только после подтверждения пользователя.
type Entry struct {
	Name        string                                                     // Имя сценария в каталоге.
	Description string                                                     // Описание перед запуском.
	Build       func(context.Context, execution.Interaction) (*Run, error) // Фабрика зависимостей, отложенная до подтверждения.
}

// Run связывает pipeline с запуском и очисткой ресурсов сценария.
// Details содержит сведения о результате, например пути сохранённых бэкапов.
type Run struct {
	Pipeline *execution.Pipeline         // Pipeline для опроса состояния.
	Execute  func(context.Context) error // Запуск с очисткой ресурсов, предусмотренной сценарием.
	Details  string                      // Сведения о результате и сохранённых файлах.
}

// requestKind различает текстовый вопрос, подтверждение и воспроизведение звука.
type requestKind uint8

const (
	askRequest     requestKind = iota // Текстовый вопрос пользователю.
	confirmRequest                    // Запрос явного согласия.
	playRequest                       // Запрос воспроизведения WAV.
)

// answer содержит текст ответа либо ошибку обработки запроса.
type answer struct {
	value string // Введённый текст или внутренний ответ yes/no.
	err   error  // Ошибка запроса либо отмена.
}

// request связывает вопрос или звук с контекстом задачи и каналом ответа.
type request struct {
	kind     requestKind        // Вид необходимого действия UI.
	ctx      context.Context    // Контекст шага, создавшего запрос.
	task     execution.TaskInfo // Задача, которой принадлежит вопрос.
	question execution.Question // Текст и параметры пользовательского ввода.
	wav      []byte             // Копия WAV, принадлежащая UI.
	reply    chan answer        // Буфер единственного ответа ожидающей задаче.
}

// readyEvent сообщает о сборке сценария; номер запуска отсеивает устаревшие события.
type readyEvent struct {
	generation int  // Номер запуска, для которого собран сценарий.
	run        *Run // Готовый сценарий для отображения состояния.
}

// playbackEvent сообщает результат звука для конкретного запроса.
type playbackEvent struct {
	request *request // Запрос, завершивший воспроизведение.
	err     error    // Ошибка проигрывателя либо отмена.
}

// logEvent передаёт запись журнала в событийный цикл.
type logEvent string

// runResult хранит собранный сценарий и итоговую ошибку его выполнения.
type runResult struct {
	run *Run  // Сценарий; nil при ошибке до завершения сборки.
	err error // Итоговая ошибка сборки или выполнения.
}

// App связывает полноэкранный UI, фоновые задачи и порт взаимодействия со сценарием.
type App struct {
	events    chan any                            // Общая очередь событий фоновых задач и терминала.
	done      chan struct{}                       // Сигнал закрытия UI для всех отправителей.
	active    atomic.Bool                         // Атомарный признак приёма запросов и логов.
	started   atomic.Bool                         // Защита от повторного запуска UI.
	playAudio func(context.Context, []byte) error // Функция воспроизведения, заменяемая в тестах.
	media     sync.WaitGroup                      // Ожидание фоновых проигрывателей перед выходом.
}

// New создаёт UI с каналами событий и системным проигрывателем.
func New() *App {
	return &App{events: make(chan any, 32), done: make(chan struct{}), playAudio: playWAV} // Создаём очередь событий, сигнал завершения и проигрыватель.
}

// newRequest привязывает запрос к текущей задаче или к подготовке сценария.
func newRequest(ctx context.Context, kind requestKind, q execution.Question) *request {
	task, ok := execution.CurrentTask(ctx) // Извлекаем метаданные текущей задачи из контекста.
	if !ok {
		task = execution.TaskInfo{ID: "setup", Name: "Подготовка сценария", Description: "Проверка настроек и сборка pipeline"}
	}
	return &request{kind: kind, ctx: ctx, task: task, question: q, reply: make(chan answer, 1)} // Буфер ответа не блокирует UI, если ожидающая задача уже отменена.
}

// Ask показывает текстовый вопрос и ждёт ответа или отмены.
func (a *App) Ask(ctx context.Context, q execution.Question) (string, error) {
	r := newRequest(ctx, askRequest, q)  // Создаём текстовый вопрос для текущей задачи.
	if err := a.enqueue(r); err != nil { // Ставим запрос задачи в очередь UI.
		return "", err
	}
	result := a.await(r) // Ждём текст ответа или причину отмены.
	return result.value, result.err
}

// Confirm запрашивает явное согласие и ждёт ответа или отмены.
func (a *App) Confirm(ctx context.Context, text string) (bool, error) {
	r := newRequest(ctx, confirmRequest, execution.Question{Text: text}) // Создаём запрос явного согласия.
	if err := a.enqueue(r); err != nil {                                 // Ставим запрос задачи в очередь UI.
		return false, err
	}
	result := a.await(r) // Ждём согласие, отказ или ошибку запроса.
	return result.value == "yes", result.err
}

// Play запрашивает запуск звука и ждёт его окончания; UI владеет отдельной копией WAV.
func (a *App) Play(ctx context.Context, wav []byte) error {
	r := newRequest(ctx, playRequest, execution.Question{Text: "Поместите станцию рядом с динамиком и включите на ней режим настройки Wi-Fi."}) // Создаём запрос воспроизведения сигнала настройки Wi-Fi.

	r.wav = bytes.Clone(wav)             // Копия живёт до конца звука, даже если шаг затрёт свой WAV.
	if err := a.enqueue(r); err != nil { // Ставим запрос задачи в очередь UI.
		clear(r.wav) // Затираем WAV после использования или отмены.
		return err
	}
	return a.await(r).err // Ждём ответ UI либо отмену запроса.
}

// enqueue передаёт запрос работающему UI с учётом отмены и завершения.
func (a *App) enqueue(r *request) error {
	if err := r.ctx.Err(); err != nil { // Проверяем отмену конкретного запроса.
		return err
	}
	if !a.active.Load() { // Проверяем, принимает ли UI события.
		return errors.New("CLI is not running") // Не ставим запрос в очередь неактивного UI.
	}
	select {
	case a.events <- r:
		return nil
	case <-r.ctx.Done(): // Ожидаем отмены запроса.
		return r.ctx.Err() // Проверяем отмену конкретного запроса.
	case <-a.done:
		return errors.New("CLI stopped") // Сообщаем ожидающей задаче о закрытии UI.
	}
}

// await ждёт ответ, отмену запроса или закрытие UI.
func (a *App) await(r *request) answer {
	select {
	case result := <-r.reply: // Получаем ответ именно на этот запрос.
		return result
	case <-r.ctx.Done(): // Ожидаем отмены запроса.
		return answer{err: r.ctx.Err()} // Проверяем отмену конкретного запроса.
	case <-a.done:
		return answer{err: errors.New("CLI stopped")} // Сообщаем ожидающей задаче о закрытии UI.
	}
}

// Write направляет логи в UI, а после его остановки пишет в stderr.
func (a *App) Write(data []byte) (int, error) {
	if a.active.Load() { // Проверяем, принимает ли UI события.
		text := string(data[:min(len(data), 4096)]) // Преобразуем символы или байты в строку.
		select {
		case a.events <- logEvent(text): // Передаём запись журнала в событийный цикл.
			return len(data), nil // Учитываем длину текущего буфера или списка.
		case <-a.done:
		}
	}
	return os.Stderr.Write(data) // После остановки UI направляем логи в stderr.
}

// Run ведёт событийный цикл, опрашивает pipeline и рисует экран.
// При выходе ждёт завершения сценария, звука и чтения ввода перед восстановлением терминала.
func (a *App) Run(ctx context.Context, entries []Entry) (result error) {
	if len(entries) == 0 { // Без каталога нечего предложить пользователю.
		return errors.New("scenario catalog is empty") // Отказываем при отсутствии сценариев.
	}
	seen := make(map[string]bool) // Создаём множество для проверки повторных элементов.
	for _, entry := range entries {
		if entry.Name == "" || entry.Build == nil || seen[entry.Name] {
			return errors.New("catalog entries need unique names and a build function") // Проверяем уникальность имён и наличие фабрики.
		}
		seen[entry.Name] = true
	}
	if !a.started.CompareAndSwap(false, true) { // Разрешаем единственный запуск приложения.
		return errors.New("CLI has already run") // Одно приложение UI допускает только один запуск.
	}
	t, err := openTerminal() // Проверяем и открываем полноэкранный терминал.
	if err != nil {
		return err
	}
	a.active.Store(true)                                            // Обновляем признак работающего UI.
	inputCtx, stopInput := context.WithCancel(context.Background()) // Чтение ввода останавливается отдельно от сценария.
	var input sync.WaitGroup                                        // Ожидание завершения фонового чтения ввода.
	input.Go(func() {                                               // Фоновая горутина читает клавиатуру до отмены.
		t.read(inputCtx, a.events) // Читаем ввод и передаём его в очередь событий.
	})
	var cancelRun context.CancelFunc // Функция отмены текущего сценария.
	var running <-chan runResult     // Канал результата; nil отключает соответствующую ветку select.
	defer func() {                   // При выходе ждём фоновые операции и восстанавливаем терминал.
		if cancelRun != nil {
			cancelRun() // Запрашиваем остановку текущего сценария.
		}
		close(a.done) // Разблокируем отправителей до ожидания их завершения.
		if running != nil {
			finished := <-running                      // Дожидаемся завершения сценария вместе с его очисткой.
			result = errors.Join(result, finished.err) // Объединяем ошибки, сохраняя каждую причину.
		}
		a.media.Wait()                          // Дожидаемся завершения проигрывателей.
		stopInput()                             // Останавливаем фоновое чтение терминала.
		input.Wait()                            // Дожидаемся остановки чтения клавиатуры.
		result = errors.Join(result, t.close()) // Восстанавливаем терминал, сохраняя также ошибку сценария.
		a.active.Store(false)                   // Обновляем признак работающего UI.
	}()
	m := newModel(entries)              // Создаём состояние экрана выбора.
	var keys decoder                    // Декодер сохраняет незавершённые последовательности ввода.
	tick := time.NewTicker(time.Second) // Создаём секундный таймер обновления состояния.
	defer tick.Stop()                   // Освобождаем таймер обновления экрана.
	escape := time.NewTimer(time.Hour)  // Создаём таймер различения Escape и последовательности клавиши.
	escape.Stop()                       // Останавливаем таймер отдельной клавиши Escape.
	defer escape.Stop()                 // Останавливаем таймер отдельной клавиши Escape.
	var escapeTick <-chan time.Time     // Канал таймера Escape; nil отключает ожидание.
	parentDone := ctx.Done()            // Канал внешней отмены; после обработки отключается через nil.
	dirty := true                       // Признак необходимости перерисовать экран.
	for {
		if dirty {
			w, h, err := t.size() // Получаем актуальные размеры терминала.
			if err != nil {
				return err
			}
			m.width, m.height = min(w-1, 500), min(h, 200) // Ограничиваем размер кадра и оставляем запас справа.
			m.prune()                                      // Убираем вопросы отменённых запросов.
			if err := t.write(m.view()); err != nil {      // Выводим кадр или управляющую команду в терминал.
				return err
			}
			dirty = false
		}
		var received []key // Клавиши, полученные за текущий проход цикла.
		select {
		case <-parentDone:
			parentDone = nil
			if running == nil {
				return ctx.Err() // Проверяем отмену или истечение срока контекста.
			}
			m.stopping, m.exitAfterRun = true, true
			cancelRun() // Запрашиваем остановку текущего сценария.
			dirty = true
		case <-tick.C:
			m.poll()  // Читаем свежий снимок pipeline.
			m.prune() // Убираем вопросы отменённых запросов.
			dirty = true
		case <-t.resize:
			dirty = true
		case <-escapeTick:
			escapeTick = nil
			received = keys.flushEscape() // Выдаём отдельный Escape после ожидания продолжения.
		case finished := <-running: // Результат завершившегося фонового сценария.
			running = nil
			cancelRun()        // Запрашиваем остановку текущего сценария.
			m.finish(finished) // Сохраняем результат и показываем экран завершения.
			dirty = true
			if m.exitAfterRun {
				return finished.err
			}
		case event := <-a.events: // Конкретный вид события из общей очереди.
			dirty = true
			switch event := event.(type) { // Конкретный вид события из общей очереди.
			case inputEvent:
				if event.err != nil {
					return event.err
				}
				received = keys.feed(event.data) // Декодируем байты ввода в клавиши.
				if keys.escapePending() {        // Проверяем, нужен ли таймаут для Escape.
					escape.Reset(45 * time.Millisecond) // Даём последовательности Escape время на продолжение.
					escapeTick = escape.C
				} else {
					escape.Stop() // Останавливаем таймер отдельной клавиши Escape.
					escapeTick = nil
				}
			case *request:
				m.add(event) // Добавляем запрос задачи в диалоги UI.
			case readyEvent:
				if event.generation == m.generation && m.phase == runningPhase {
					m.run = event.run
					m.poll() // Читаем свежий снимок pipeline.
				}
			case playbackEvent:
				m.resolve(event.request, answer{err: event.err}) // Закрываем диалог и возвращаем ответ его задаче.
			case logEvent:
				m.log(string(event)) // Добавляем запись в ограниченный журнал UI.
			}
		}
		for _, key := range received {
			dirty = true
			switch m.handleKey(key) { // Обрабатываем клавишу и получаем действие приложения.
			case startAction:
				runCtx, cancel := context.WithCancel(ctx) // Создаём отмену только для выбранного запуска.
				cancelRun = cancel
				running = a.start(runCtx, m.entries[m.selection], m.generation) // Запускаем выбранный сценарий в фоне.
			case stopAction:
				if cancelRun != nil {
					cancelRun() // Запрашиваем остановку текущего сценария.
				}
			case quitAction:
				if running == nil {
					return m.err
				}
				m.stopping, m.exitAfterRun = true, true
				cancelRun() // Запрашиваем остановку текущего сценария.
			case playAction:
				dialog := m.currentDialog() // Находим вопрос, которому принадлежит фокус ввода.
				if dialog != nil && !dialog.playing {
					dialog.playing = true
					r := dialog.request // Запрос, которому принадлежит текущий диалог.
					data := r.wav       // Копия WAV, которой владеет UI до конца проигрывания.
					a.media.Go(func() { // Отслеживаем проигрывание, чтобы дождаться его при выходе.
						err := a.playAudio(r.ctx, data) // Играем принадлежащую UI копию WAV до конца или отмены.
						clear(data)                     // Затираем WAV после использования или отмены.
						select {
						case a.events <- playbackEvent{request: r, err: err}:
						case <-a.done:
						}
					})
				}
			}
		}
	}
}

// start собирает и запускает сценарий в горутине; буфер результата позволяет завершиться без читателя.
func (a *App) start(ctx context.Context, entry Entry, generation int) <-chan runResult {
	done := make(chan runResult, 1) // Буфер одного результата не задерживает завершение отправителя.
	go func() {                     // Собираем и выполняем сценарий в отдельной горутине.
		result := runResult{} // Результат сборки и выполнения фонового сценария.
		defer func() {        // Перехватываем панику и всегда публикуем результат запуска.
			if value := recover(); value != nil { // Перехватываем панику для возврата обычной ошибки.
				result.err = fmt.Errorf("scenario panicked: %v", value) // Добавляем пояснение к возвращаемой ошибке.
			}
			done <- result
		}()
		if err := ctx.Err(); err != nil { // Проверяем отмену или истечение срока контекста.
			result.err = err
			return
		}
		result.run, result.err = entry.Build(ctx, a) // Собираем сценарий только после подтверждения пользователя.
		if result.err != nil {
			return
		}
		if result.run == nil || result.run.Pipeline == nil || result.run.Execute == nil {
			result.err = errors.New("scenario builder returned an incomplete run") // Не запускаем сценарий без pipeline или функции выполнения.
			return
		}
		select {
		case a.events <- readyEvent{generation: generation, run: result.run}:
		case <-ctx.Done(): // Ожидаем отмены контекста.
			result.err = ctx.Err() // Проверяем отмену или истечение срока контекста.
			return
		case <-a.done:
			result.err = errors.New("CLI stopped") // Сообщаем ожидающей задаче о закрытии UI.
			return
		}
		result.err = result.run.Execute(ctx) // Запускаем сценарий вместе с предусмотренной им очисткой.
	}()
	return done
}
