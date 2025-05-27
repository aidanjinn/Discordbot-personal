package bot

import (
	"context"
	"fmt"
	"google.golang.org/genai"
	"os"
)

func getGeminiResponse(ctx context.Context, prompt string) (string, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("GEMINI_API_KEY is not set")
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})

	if err != nil {
		return "", fmt.Errorf("failed to create Gemini client: %w", err)
	}

	promptIntro := "You are a crazy schizophrenia, conspiracy believing, paranoid old chinese man named Wang, and you speak with a fuckton curse words\n"
	// promptTheories := "Here is a list of theories : +
	// {alien's bring bigfoot to earth 10000 years ago to protect them from dinosaurs while the mined gold on earth, elites buying children to harvest adrenochrome, MKUltra," +
	//	" The government poisoning the water to turn the youth and frogs gay, Agartha, anunnaki, and babylonians}" +
	//	" : END OF EXAMPLES " +
	//	": BRAIN AND THOUGHT PROCESSES : {In your response DO NOT just use one or all the examples given; Take those examples, using your LLM database of information (on google) and come " +
	//	"  up and respond with different crazy ideas I want your response to have a proper conclusion and if asked a QUESTION given AN ANSWER to it}\n"
	promptEnd := "Return a crazy response to this statement prompt:{" + prompt + "} " +
		"with a statement you would say : (only the response : make sure your RESPONSE IS UNDER 3000 characters)\n"
	totalPrompt := promptIntro + promptEnd

	result, err := client.Models.GenerateContent(
		ctx,
		"gemini-2.0-flash",
		genai.Text(totalPrompt),
		nil,
	)
	if err != nil {
		return "", fmt.Errorf("failed to generate content: %w", err)
	}

	response := result.Text()

	if response == "" {
		return "", fmt.Errorf("empty response from Gemini")
	}

	return response, nil
}

func imageProcess(ctx context.Context, imagePath string, prompt string) (string, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("GEMINI_API_KEY is not set")
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create Gemini client: %w", err)
	}

	myfile, err := client.Files.UploadFromPath(ctx, imagePath, &genai.UploadFileConfig{
		MIMEType: "image/jpeg", // change if needed
	})
	if err != nil {
		return "", fmt.Errorf("failed to upload image: %w", err)
	}

	promptIntro := "You are a crazy schizophrenia, conspiracy believing, paranoid old chinese man named Wang, and you speak with a fuckton curse words\n"
	finalPrompt := promptIntro + prompt + "{Keep your response under 3000 characters}"

	parts := []*genai.Part{
		genai.NewPartFromURI(myfile.URI, myfile.MIMEType),
		genai.NewPartFromText("\n\n"),
		genai.NewPartFromText(finalPrompt),
	}

	contents := []*genai.Content{
		genai.NewContentFromParts(parts, "user"),
	}

	response, err := client.Models.GenerateContent(ctx, "gemini-2.0-flash", contents, nil)
	if err != nil {
		return "", fmt.Errorf("Gemini generation error: %w", err)
	}

	return response.Text(), nil
}

func generateImageFromPrompt(ctx context.Context, prompt string, imagePath string) (string, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("GEMINI_API_KEY is not set")
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create Gemini client: %w", err)
	}

	var parts []*genai.Part

	// If an image is attached, upload it and include as a reference
	if imagePath != "" {
		mimeType := "image/jpeg"

		uploaded, err := client.Files.UploadFromPath(ctx, imagePath, &genai.UploadFileConfig{
			MIMEType: mimeType,
		})
		if err != nil {
			return "", fmt.Errorf("failed to upload image: %w", err)
		}
		parts = append(parts, genai.NewPartFromURI(uploaded.URI, mimeType))
	}

	parts = append(parts, genai.NewPartFromText(prompt))

	contents := []*genai.Content{
		genai.NewContentFromParts(parts, genai.RoleUser),
	}

	// Specify that you want image output
	config := &genai.GenerateContentConfig{
		ResponseModalities: []string{"TEXT", "IMAGE"},
	}

	result, err := client.Models.GenerateContent(
		ctx,
		"gemini-2.0-flash-preview-image-generation",
		contents,
		config,
	)

	// If this happens most likely gemini was censored from returning an image
	// Either due to the prompt, image given, or the output (ex: NSFW)
	if err != nil {
		return "", fmt.Errorf("Gemini image generation error: %w", err)
	}

	var outputFilename string

	if len(result.Candidates) == 0 || result.Candidates[0].Content == nil {
		return "", fmt.Errorf("no content in Gemini response")
	}

	for _, part := range result.Candidates[0].Content.Parts {
		if part.Text != "" {
			fmt.Println("Text response:", part.Text)
		} else if part.InlineData != nil {
			imageBytes := part.InlineData.Data
			outputFilename = "gemini_generated_image.png"
			err := os.WriteFile(outputFilename, imageBytes, 0644)
			if err != nil {
				return "", fmt.Errorf("failed to write image: %w", err)
			}
		}
	}

	if outputFilename == "" {
		return "", fmt.Errorf("no image was generated")
	}

	return outputFilename, nil
}
