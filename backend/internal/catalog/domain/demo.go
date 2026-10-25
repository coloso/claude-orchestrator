package domain

// DemoCatalog returns the demo catalog as drafts: a fixed list of real books
// that can be seeded into an environment, in a fixed order. Each call returns
// fresh values.
func DemoCatalog() []BookDraft {
	return []BookDraft{
		demoBook("9780321125217", "Domain-Driven Design", []string{"Eric Evans"}, 2003, "en", 560),
		demoBook("9780321834577", "Implementing Domain-Driven Design", []string{"Vaughn Vernon"}, 2013, "en", 656),
		demoBook("9780321127426", "Patterns of Enterprise Application Architecture", []string{"Martin Fowler"}, 2002, "en", 560),
		demoBook("9780134757599", "Refactoring", []string{"Martin Fowler"}, 2018, "en", 448),
		demoBook("9780132350884", "Clean Code", []string{"Robert C. Martin"}, 2008, "en", 464),
		demoBook("9780135957059", "The Pragmatic Programmer", []string{"David Thomas", "Andrew Hunt"}, 2019, "en", 352),
		demoBook("9781449373320", "Designing Data-Intensive Applications", []string{"Martin Kleppmann"}, 2017, "en", 616),
		demoBook("9780134190440", "The Go Programming Language", []string{"Alan A. A. Donovan", "Brian W. Kernighan"}, 2015, "en", 380),
		demoBook("9781942788331", "Accelerate", []string{"Nicole Forsgren", "Jez Humble", "Gene Kim"}, 2018, "en", 288),
		demoBook("9780262510875", "Structure and Interpretation of Computer Programs", []string{"Harold Abelson", "Gerald Jay Sussman"}, 1996, "en", 657),
		// The last two leave optional fields empty on purpose.
		{ISBN: "9780465026562", Title: "Gödel, Escher, Bach", Authors: []string{"Douglas R. Hofstadter"}, PublishedYear: new(1999), Language: new("en")},
		{ISBN: "9782070612758", Title: "Le Petit Prince", Authors: []string{"Antoine de Saint-Exupéry"}, Language: new("fr")},
	}
}

func demoBook(isbn, title string, authors []string, year int, lang string, pages int) BookDraft {
	return BookDraft{ISBN: isbn, Title: title, Authors: authors, PublishedYear: &year, Language: &lang, Pages: &pages}
}
