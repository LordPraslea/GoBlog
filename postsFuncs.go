package main

import (
	"html/template"
	"log"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

func (p *post) fullURL() string {
	return appConfig.Server.PublicAddress + p.Path
}

func (p *post) shortURL() string {
	s, err := shortenPath(p.Path)
	if err != nil {
		return ""
	}
	if appConfig.Server.ShortPublicAddress != "" {
		return appConfig.Server.ShortPublicAddress + s
	}
	return appConfig.Server.PublicAddress + s
}

func (p *post) firstParameter(parameter string) (result string) {
	if pp := p.Parameters[parameter]; len(pp) > 0 {
		result = pp[0]
	}
	return
}

func (p *post) title() string {
	return p.firstParameter("title")
}

func (p *post) html() template.HTML {
	if p.rendered != "" {
		return p.rendered
	}
	htmlContent, err := renderMarkdown(p.Content, false)
	if err != nil {
		log.Fatal(err)
		return ""
	}
	p.rendered = template.HTML(htmlContent)
	return p.rendered
}

func (p *post) absoluteHTML() template.HTML {
	if p.absoluteRendered != "" {
		return p.absoluteRendered
	}
	htmlContent, err := renderMarkdown(p.Content, true)
	if err != nil {
		log.Fatal(err)
		return ""
	}
	p.absoluteRendered = template.HTML(htmlContent)
	return p.absoluteRendered
}

const summaryDivider = "<!--more-->"

func (p *post) summary() (summary string) {
	summary = p.firstParameter("summary")
	if summary != "" {
		return
	}
	html := string(p.html())
	if splitted := strings.Split(html, summaryDivider); len(splitted) > 1 {
		doc, _ := goquery.NewDocumentFromReader(strings.NewReader(splitted[0]))
		summary = doc.Text()
	} else {
		doc, _ := goquery.NewDocumentFromReader(strings.NewReader(html))
		summary = doc.Find("p").First().Text()
	}
	return
}

func (p *post) translations() []*post {
	translationkey := p.firstParameter("translationkey")
	if translationkey == "" {
		return nil
	}
	posts, err := getPosts(&postsRequestConfig{
		parameter:      "translationkey",
		parameterValue: translationkey,
	})
	if err != nil || len(posts) == 0 {
		return nil
	}
	translations := []*post{}
	for _, t := range posts {
		if p.Path != t.Path {
			translations = append(translations, t)
		}
	}
	if len(translations) == 0 {
		return nil
	}
	return translations
}

func (p *post) isPublishedSectionPost() bool {
	return p.Published != "" && p.Section != "" && p.Status == statusPublished
}
