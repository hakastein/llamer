package drivers

// Settings — программа драйвера и её окружение из конфига.
type Settings struct {
	Bin string
	Env [][2]string // переменные сверх белого списка, в порядке конфига
	// Readable — каталоги, открытые строгой песочнице всегда: сам драйвер и его рантайм.
	Readable []string
}
