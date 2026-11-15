package ui

import (
	"fmt"
	"strings"

	"github.com/fr33dman/yandex-station-picklock/pkg/execution"
)

var headerLogo = []string{ // Строки большого ASCII-логотипа шапки.
	`         .+oO@@@Oo+.          ______   __     ______     __  __     __         ______     ______     __  __     ______     ______    `,
	`       .o@@@@OOO@@@@o.       /\  == \ /\ \   /\  ___\   /\ \/ /    /\ \       /\  __ \   /\  ___\   /\ \/ /    /\  ___\   /\  == \   `,
	`      o@@@O+.   .+O@@@o      \ \  _-/ \ \ \  \ \ \____  \ \  _"-.  \ \ \____  \ \ \/\ \  \ \ \____  \ \  _"-.  \ \  __\   \ \  __<   `,
	`    .O@@@+         +@@@O.     \ \_\    \ \_\  \ \_____\  \ \_\ \_\  \ \_____\  \ \_____\  \ \_____\  \ \_\ \_\  \ \_____\  \ \_\ \_\ `,
	`   +@@@O.           .O@@@+     \/_/     \/_/   \/_____/   \/_/\/_/   \/_____/   \/_____/   \/_____/   \/_/\/_/   \/_____/   \/_/ /_/ `,
	`  +@@@o               o@@@+                                                                                                          `,
	` +@@@o                 o@@@+     ALICE · YANDEX STATION · PICKLOCKER                                                                 `,
	`.@@@o                   o@@@.                                                                                                        `,
	`O@@O                     @@@O                                                                                                        `,
	`@@@o                     o@@@                                                                                                        `,
	`O@@O.                   .O@@O                                                                                                        `,
	`.O@@@O+..           ..+O@@@O.                                                                                                        `,
	`  +O@@@@@@@OOOOOOO@@@@@@@O+                                                                                                          `,
	`     .+oOO@@@@@@@@@OOo+.                                                                                                             `,
}

// header рисует логотипы по размеру терминала и возвращает отступ для содержимого.
func header(c *canvas) int {
	if c.w >= 133 && c.h >= 14 { // Полный вариант шапки требует широкого терминала.
		for i := range headerLogo { // Обходим строки большого логотипа.
			c.text(1, i, 12, aliceLogo[i], purple)     // Рисуем текст в пределах указанной области.
			c.text(15, i, c.w-16, wordmark[i], purple) // Рисуем текст в пределах указанной области.
		}
		return 6
	}
	c.text(1, 0, c.w-2, `  /\    / Picklocker /`, purple)        // Рисуем текст в пределах указанной области.
	c.text(1, 1, c.w-2, ` /__\   ALICE · YANDEX STATION`, muted) // Рисуем текст в пределах указанной области.
	return 3
}

// view собирает кадр текущего экрана, шапку и подсказки клавиш.
func (m *model) view() string {
	c := newCanvas(m.width, m.height) // Создаём сетку нового экранного кадра.
	if m.width < 50 || m.height < 16 {
		c.paragraph(rect{x: 1, y: 1, w: max(1, c.w-2), h: c.h - 2}, "Picklocker\nУвеличьте окно минимум до 50×16.\nCtrl+C — безопасный выход.", yellow) // Рисуем текст с переносами в доступной области.
		return c.frame()                                                                                                                                // Собираем готовый терминальный кадр.
	}
	top := header(c)                                     // Шапка сообщает отступ для основного содержимого.
	body := rect{x: 0, y: top, w: c.w, h: c.h - top - 1} // Содержимое между шапкой и нижними подсказками.
	switch m.phase {
	case choosePhase:
		m.drawChooser(c, body) // Рисуем список сценариев.
	case confirmPhase:
		m.drawConfirmation(c, body) // Рисуем подтверждение запуска.
	default:
		m.drawRun(c, body) // Рисуем выполнение либо итог сценария.
	}
	help := "↑/↓ выбрать · Enter продолжить · q выйти" // Подсказка клавиш для текущего экрана.
	if m.phase == confirmPhase {
		help = "←/→ выбор · Enter подтвердить · y да / n нет · Esc назад"
	}
	if m.phase == runningPhase {
		help = "Tab задачи · PgUp/PgDn граф · Home текущий шаг · Esc отмена · Ctrl+C выход"
	}
	if m.phase == finishedPhase {
		help = "Enter каталог · PgUp/PgDn граф · q выход"
	}
	if m.currentDialog() != nil { // Находим вопрос, которому принадлежит фокус ввода.
		help = "Enter ответить · Tab следующий вопрос · PgUp/PgDn текст · Esc отмена"
	}
	if m.stopping {
		help = "Отмена запрошена. Ожидаем завершения начатых операций и очистки ресурсов…"
	}
	c.text(1, c.h-1, c.w-2, help, muted) // Рисуем текст в пределах указанной области.
	return c.frame()                     // Собираем готовый терминальный кадр.
}

// drawChooser рисует прокручиваемый список сценариев и выделяет выбранный.
func (m *model) drawChooser(c *canvas, area rect) {
	c.box(area, "Сценарии", purple)                                               // Рисуем рамку панели и её заголовок.
	c.text(area.x+2, area.y+1, area.w-4, "Выберите сценарий для запуска", normal) // Рисуем текст в пределах указанной области.
	visible := max(1, (area.h-6)/3)                                               // Число пунктов меню, помещающихся по высоте.
	start := max(0, m.selection-visible+1)                                        // Оставляем выбранный пункт в видимой части списка.
	for i := start; i < min(len(m.entries), start+visible); i++ {                 // Ограничиваем размер доступной областью.
		y := area.y + 3 + (i-start)*3 // Вертикальная координата следующей строки.
		style := normal               // Стиль обычного или выделенного элемента.
		prefix := "  "                // Маркер выбранного пункта меню.
		if i == m.selection {
			style = selected
			prefix = "> "
		}
		c.text(area.x+2, y, area.w-4, prefix+m.entries[i].Name, style)                    // Рисуем текст в пределах указанной области.
		c.text(area.x+4, y+1, area.w-6, short(m.entries[i].Description, area.w-6), muted) // Рисуем текст в пределах указанной области.
	}
	c.text(area.x+2, area.y+area.h-2, area.w-4, fmt.Sprintf("%d / %d · Сборка и подключение к устройству начнутся после подтверждения", m.selection+1, len(m.entries)), muted) // Рисуем текст в пределах указанной области.
}

// drawConfirmation показывает описание сценария и выбор подтверждения.
func (m *model) drawConfirmation(c *canvas, area rect) {
	entry := m.entries[m.selection]                   // Выбранный пользователем сценарий каталога.
	c.box(area, "Подтверждение запуска", purple)      // Рисуем рамку панели и её заголовок.
	y := area.y + 2                                   // Вертикальная координата следующей строки.
	c.text(area.x+2, y, area.w-4, entry.Name, normal) // Рисуем текст в пределах указанной области.
	y += 2
	y += c.paragraph(rect{x: area.x + 2, y: y, w: area.w - 4, h: max(1, area.h-8)}, entry.Description, muted) // Рисуем текст с переносами в доступной области.
	y = min(y+1, area.y+area.h-4)                                                                             // Ограничиваем размер доступной областью.
	c.text(area.x+2, y, area.w-4, "Запустить выбранный сценарий?", normal)                                    // Рисуем текст в пределах указанной области.
	choices(c, area.x+2, area.y+area.h-2, area.w-4, m.yes)                                                    // Рисуем варианты подтверждения с текущим выбором.
}

// choices рисует варианты «Нет» и «Да», выделяя текущий выбор.
func choices(c *canvas, x, y, columns int, yes bool) {
	noStyle, yesStyle := selected, normal // Стили вариантов отказа и согласия.
	if yes {
		noStyle, yesStyle = normal, selected
	}
	c.text(x, y, min(columns, 10), " Нет [n] ", noStyle) // Рисуем текст в пределах указанной области.
	if columns > 12 {
		c.text(x+12, y, columns-12, " Да [y] ", yesStyle) // Рисуем текст в пределах указанной области.
	}
}

// drawRun рисует граф pipeline, панели задач и текущий диалог.
func (m *model) drawRun(c *canvas, area rect) {
	entry := m.entries[m.selection]                                             // Выбранный пользователем сценарий каталога.
	lines := graph(m.snapshot.Jobs, area.w-4)                                   // Строим строки графа из снимков задач.
	description := wrap(entry.Description, area.w-4)                            // Разбиваем текст на строки доступной ширины.
	graphHeight := min(max(6, len(description)+len(lines)+4), max(5, area.h/2)) // Ограничиваем размер доступной областью.
	graphArea := rect{x: area.x, y: area.y, w: area.w, h: graphHeight}          // Прямоугольник панели графа.
	stateStyle := purple                                                        // Цвет общего состояния сценария.
	if m.err != nil {
		stateStyle = red
		if execution.IsCancellation(m.err) { // Отличаем чистую отмену от реального сбоя.
			stateStyle = yellow
		}
	}
	c.box(graphArea, m.outcome(), stateStyle)                // Рисуем рамку панели и её заголовок.
	c.text(area.x+2, area.y+1, area.w-4, entry.Name, normal) // Рисуем текст в пределах указанной области.
	descHeight := min(1, len(description))                   // Ограничиваем размер доступной областью.
	for i := 0; i < descHeight; i++ {                        // Рисуем только видимую часть описания сценария.
		c.text(area.x+2, area.y+2+i, area.w-4, description[i], muted) // Рисуем текст в пределах указанной области.
	}
	y := area.y + 2 + descHeight                  // Вертикальная координата следующей строки.
	available := max(0, graphHeight-3-descHeight) // Ограничиваем размер доступной областью.
	if len(lines) == 0 {                          // Проверяем количество доступных элементов.
		c.text(area.x+2, y, area.w-4, "[> Подготовка] → [Pipeline]", yellow) // Рисуем текст в пределах указанной области.
	} else {
		offset := min(m.graphOffset, max(0, len(lines)-available)) // Ограничиваем прокрутку доступными строками.
		if m.followGraph {
			for i, line := range lines {
				if line.running {
					offset = min(max(0, i-available/2), max(0, len(lines)-available)) // Ограничиваем прокрутку доступными строками.
					break
				}
			}
		}
		for row, line := range lines[offset:min(len(lines), offset+available)] { // Ограничиваем прокрутку доступными строками.
			x := area.x + 2 // Горизонтальная координата следующего фрагмента.
			for _, part := range line.parts {
				c.text(x, y+row, area.x+area.w-2-x, part.text, part.style) // Рисуем текст в пределах указанной области.
				x += textWidth(part.text)                                  // Считаем ширину текста в ячейках терминала.
			}
		}
		if len(lines) > available { // Проверяем количество доступных элементов.
			c.text(area.x+area.w-17, area.y+graphHeight-1, 14, fmt.Sprintf(" %d–%d/%d ", offset+1, min(len(lines), offset+available), len(lines)), muted) // Рисуем текст в пределах указанной области.
		}
	}
	tasks := m.tasks()                            // Выбираем задачи для текущих панелей.
	page := m.taskPage % max(1, (len(tasks)+3)/4) // Циклически выбираем страницу из групп по четыре задачи.
	if d := m.currentDialog(); d != nil {         // Находим вопрос, которому принадлежит фокус ввода.
		for i, task := range tasks {
			if task.id == d.request.task.ID {
				page = i / 4
				break
			}
		}
	}
	first := page * 4                                                                        // Первый индекс текущей четвёрки задач.
	visible := tasks[first:min(len(tasks), first+4)]                                         // Ограничиваем размер доступной областью.
	taskArea := rect{x: area.x, y: area.y + graphHeight, w: area.w, h: area.h - graphHeight} // Всё оставшееся место под панели задач.
	for i, r := range taskRects(taskArea, len(visible)) {                                    // Делим доступную область между видимыми задачами.
		m.drawTask(c, r, visible[i]) // Рисуем содержимое одной панели задачи.
	}
	if d := m.currentDialog(); d != nil && !m.stopping { // Находим вопрос, которому принадлежит фокус ввода.
		m.drawDialog(c, area, d) // Рисуем текущий вопрос поверх панелей.
	}
}

// drawTask рисует имя, описание, статус и доступные сообщения задачи.
func (m *model) drawTask(c *canvas, area rect, task taskView) {
	_, style := statusLabel(task.status)                                      // Подбираем подпись и цвет статуса.
	c.box(area, taskTitle(task), style)                                       // Рисуем рамку панели и её заголовок.
	inner := rect{x: area.x + 2, y: area.y + 1, w: area.w - 4, h: area.h - 2} // Область содержимого внутри рамки задачи.
	if inner.h < 1 || inner.w < 1 {
		return
	}
	used := c.paragraph(rect{x: inner.x, y: inner.y, w: inner.w, h: min(3, inner.h)}, task.description, muted) // Рисуем текст с переносами в доступной области.
	inner.y += used + 1
	inner.h -= used + 1
	text := "Ожидание выполнения" // Текст, подготовленный для текущего действия.
	if task.status == "running" {
		text = "Выполняется…"
	}
	if task.err != "" {
		text = safeText(task.err) // Ограничиваем текст перед отрисовкой.
	}
	if m.phase == finishedPhase && m.err != nil {
		text = safeText(m.err.Error()) // Ограничиваем текст перед отрисовкой.
	}
	if m.phase == finishedPhase && m.err == nil && m.run != nil {
		text = "Готово.\n" + m.run.Details
	}
	if m.stopping {
		text = "Запрошена отмена. Уже начатая запись boot завершится вместе с проверкой readback."
	}
	if len(m.messages) > 0 && task.err == "" && m.phase == runningPhase { // Показываем последние события только при выполнении без ошибки шага.
		text += "\n\nСобытия приложения:\n" + strings.Join(m.messages[max(0, len(m.messages)-2):], "\n") // Соединяем строки сообщений.
	}
	c.paragraph(inner, text, normal) // Рисуем текст с переносами в доступной области.
}

// drawDialog рисует вопрос поверх панелей, сохраняя видимость текста и курсора.
func (m *model) drawDialog(c *canvas, body rect, d *dialog) {
	columns := max(10, body.w-8)                                                           // Ограничиваем размер доступной областью.
	question := wrap(d.request.question.Text, columns)                                     // Разбиваем текст на строки доступной ширины.
	height := min(body.h-2, max(9, len(question)+7))                                       // Диалог вмещает вопрос, но остаётся в пределах экрана.
	area := rect{x: body.x + 2, y: body.y + body.h - height - 1, w: body.w - 4, h: height} // Область текущего диалога.

	for y := area.y; y < area.y+area.h; y++ { // Очищаем фон диалога; длинный вопрос может перекрыть граф.
		c.text(area.x, y, area.w, strings.Repeat(" ", area.w), normal) // Рисуем текст в пределах указанной области.
	}
	c.box(area, d.request.task.Name, purple)                                               // Рисуем рамку панели и её заголовок.
	c.text(area.x+2, area.y+1, columns, short(d.request.task.Description, columns), muted) // Рисуем текст в пределах указанной области.
	rows := max(1, area.h-6)                                                               // Ограничиваем размер доступной областью.
	offset := min(d.scroll, max(0, len(question)-rows))                                    // Ограничиваем прокрутку доступными строками.
	for i, line := range question[offset:min(len(question), offset+rows)] {                // Ограничиваем прокрутку доступными строками.
		c.text(area.x+2, area.y+2+i, columns, line, normal) // Рисуем текст в пределах указанной области.
	}
	if len(question) > rows { // Проверяем количество доступных элементов.
		c.text(area.x+2, area.y+area.h-4, columns, fmt.Sprintf("Текст %d–%d/%d · PgUp/PgDn", offset+1, min(len(question), offset+rows), len(question)), muted) // Рисуем текст в пределах указанной области.
	}
	y := area.y + area.h - 3 // Вертикальная координата следующей строки.
	switch d.request.kind {
	case confirmRequest:
		choices(c, area.x+2, y, columns, d.yes) // Рисуем варианты подтверждения с текущим выбором.
	case playRequest:
		text := "Enter — воспроизвести сигнал Wi-Fi" // Текст, подготовленный для текущего действия.
		if d.playing {
			text = "Воспроизведение…"
		}
		c.text(area.x+2, y, columns, text, cyan) // Рисуем текст в пределах указанной области.
	case askRequest:
		text := string(d.text) // Готовим ответ к рисованию, затем при необходимости маскируем.
		cursor := d.cursor     // Позиция курсора в символах, а не байтах.
		if d.request.question.Secret {
			text = strings.Repeat("*", len(d.text)) // Секрет заменяем звёздочками той же длины.
		}
		runes := []rune(text)            // Разбираем текст на Unicode-символы.
		before := string(runes[:cursor]) // Преобразуем символы или байты в строку.
		after := string(runes[cursor:])  // Преобразуем символы или байты в строку.

		for textWidth(before) > columns-3 { // Сдвигаем текст, сохраняя курсор и окружение в поле ввода.
			_, size := firstRune(before) // Узнаём длину первого символа для сдвига видимого ввода.
			before = before[size:]
		}
		c.text(area.x+2, y, columns, "> "+before, cyan) // Рисуем текст в пределах указанной области.
		x := area.x + 4 + textWidth(before)             // Считаем ширину текста в ячейках терминала.
		c.text(x, y, 1, " ", selected)                  // Рисуем текст в пределах указанной области.
		if after != "" {
			c.text(x+1, y, max(0, columns-(x-area.x-2)-1), after, cyan) // Рисуем текст в пределах указанной области.
		}
	}
	if len(m.dialogs) > 1 { // Проверяем количество доступных элементов.
		c.text(area.x+2, area.y+area.h-2, columns, fmt.Sprintf("Вопрос %d/%d · Tab переключить задачу", m.focus+1, len(m.dialogs)), muted) // Рисуем текст в пределах указанной области.
	}
}

// firstRune возвращает первый символ строки и его длину в байтах.
func firstRune(text string) (rune, int) {
	for _, r := range text {
		return r, len(string(r)) // Учитываем длину текущего буфера или списка.
	}
	return 0, 0
}
