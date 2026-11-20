package drivers

// Level — модель и усилие одного запуска.
type Level struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

// Settings — исполняемая программа, её окружение и модели.
type Settings struct {
	Bin    string
	Env    [][2]string
	Levels []Level
	// Readable — каталоги, которые нужны песочнице `llamer run` всегда: сам драйвер и его рантайм.
	Readable []string
}
