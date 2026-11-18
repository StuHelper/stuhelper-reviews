package reviews

// RatingDimensionKeys defines the canonical 0..4 index order of the five course rating dimensions:
// 0=recommendation, 1=content_quality, 2=difficulty, 3=workload, 4=grading.
var RatingDimensionKeys = [5]string{
	"recommendation",
	"content_quality",
	"difficulty",
	"workload",
	"grading",
}

// SupportedLocales lists the eight required application locales, including Arabic (ar-EG, RTL).
var SupportedLocales = [8]string{
	"zh-CN",
	"en-US",
	"ja-JP",
	"ko-KR",
	"fr-FR",
	"es-ES",
	"ru-RU",
	"ar-EG",
}

var defaultRatingTextsTemplate = map[string][5][6]string{
	"zh-CN": {
		{"暂无足够数据", "强烈不推荐", "不推荐", "一般", "推荐", "强烈推荐"},
		{"暂无足够数据", "很差", "较差", "一般", "良好", "优秀"},
		{"暂无足够数据", "很容易", "较容易", "适中", "较难", "很难"},
		{"暂无足够数据", "很轻松", "较轻松", "适中", "较繁重", "很繁重"},
		{"暂无足够数据", "很严苛", "较严苛", "一般", "较好", "很优厚"},
	},
	"en-US": {
		{"Not enough ratings", "Strongly not recommended", "Not recommended", "Average", "Recommended", "Strongly recommended"},
		{"Not enough ratings", "Very poor", "Below average", "Fair", "Good", "Excellent"},
		{"Not enough ratings", "Very easy", "Easy", "Moderate", "Hard", "Very hard"},
		{"Not enough ratings", "Very light", "Light", "Moderate", "Heavy", "Very heavy"},
		{"Not enough ratings", "Very strict", "Strict", "Fair", "Generous", "Very generous"},
	},
	"ja-JP": {
		{"評価データ不足", "全くおすすめしない", "おすすめしない", "普通", "おすすめ", "強くおすすめ"},
		{"評価データ不足", "非常に低い", "やや低い", "普通", "良い", "非常に良い"},
		{"評価データ不足", "非常に易しい", "やや易しい", "普通", "やや難しい", "非常に難しい"},
		{"評価データ不足", "非常に少ない", "やや少ない", "普通", "やや多い", "非常に多い"},
		{"評価データ不足", "非常に厳しい", "やや厳しい", "普通", "やや寛大", "非常に寛大"},
	},
	"ko-KR": {
		{"평가 데이터 부족", "매우 비추천", "비추천", "보통", "추천", "매우 추천"},
		{"평가 데이터 부족", "매우 미흡", "미흡", "보통", "우수", "매우 우수"},
		{"평가 데이터 부족", "매우 쉬움", "쉬움", "보통", "어려움", "매우 어려움"},
		{"평가 데이터 부족", "매우 적음", "적음", "보통", "많음", "매우 많음"},
		{"평가 데이터 부족", "매우 엄격함", "엄격함", "보통", "후함", "매우 후함"},
	},
	"fr-FR": {
		{"Données insuffisantes", "Fortement déconseillé", "Déconseillé", "Moyen", "Recommandé", "Fortement recommandé"},
		{"Données insuffisantes", "Très faible", "Faible", "Correcte", "Bonne", "Excellente"},
		{"Données insuffisantes", "Très facile", "Facile", "Modérée", "Difficile", "Très difficile"},
		{"Données insuffisantes", "Très légère", "Légère", "Modérée", "Importante", "Très importante"},
		{"Données insuffisantes", "Très sévère", "Sévère", "Équilibrée", "Bienveillante", "Très généreuse"},
	},
	"es-ES": {
		{"Datos insuficientes", "Muy poco recomendable", "Poco recomendable", "Normal", "Recomendable", "Muy recomendable"},
		{"Datos insuficientes", "Muy baja", "Baja", "Aceptable", "Buena", "Excelente"},
		{"Datos insuficientes", "Muy fácil", "Fácil", "Moderada", "Difícil", "Muy difícil"},
		{"Datos insuficientes", "Muy ligera", "Ligera", "Moderada", "Alta", "Muy alta"},
		{"Datos insuficientes", "Muy estricta", "Estricta", "Justa", "Generosa", "Muy generosa"},
	},
	"ru-RU": {
		{"Недостаточно оценок", "Крайне не рекомендую", "Не рекомендую", "Средне", "Рекомендую", "Настоятельно рекомендую"},
		{"Недостаточно оценок", "Очень низкое", "Низкое", "Среднее", "Хорошее", "Отличное"},
		{"Недостаточно оценок", "Очень легко", "Легко", "Умеренно", "Сложно", "Очень сложно"},
		{"Недостаточно оценок", "Очень малая", "Малая", "Умеренная", "Большая", "Очень большая"},
		{"Недостаточно оценок", "Очень строго", "Строго", "Справедливо", "Лояльно", "Очень лояльно"},
	},
	"ar-EG": {
		{"بيانات غير كافية", "لا يُنصح به إطلاقًا", "لا يُنصح به", "متوسط", "يُنصح به", "يُنصح به بشدة"},
		{"بيانات غير كافية", "ضعيفة جدًا", "ضعيفة", "مقبولة", "جيدة", "ممتازة"},
		{"بيانات غير كافية", "سهل جدًا", "سهل", "متوسط الصعوبة", "صعب", "صعب جدًا"},
		{"بيانات غير كافية", "خفيف جدًا", "خفيف", "معتدل", "كثيف", "كثيف جدًا"},
		{"بيانات غير كافية", "صارم جدًا", "صارم", "متوازن", "مُيسَّر", "سخي جدًا"},
	},
}

// DefaultRatingTexts returns a fresh deep copy of the eight-locale [5][6] default rating label template.
func DefaultRatingTexts() map[string][][]string {
	out := make(map[string][][]string, len(defaultRatingTextsTemplate))
	for locale, dims := range defaultRatingTextsTemplate {
		copiedDims := make([][]string, 5)
		for d := range 5 {
			scores := make([]string, 6)
			copy(scores, dims[d][:])
			copiedDims[d] = scores
		}
		out[locale] = copiedDims
	}
	return out
}
