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
	//promptTheories := "Here is a list of theories : {alien's bring bigfoot to earth 10000 years ago to protect them from dinosaurs while the mined gold on earth, elites buying children to harvest adrenochrome, MKUltra," +
	//	" The government poisoning the water to turn the youth and frogs gay, Agartha, anunnaki, and babylonians}" +
	//	" : END OF EXAMPLES " +
	//	": BRAIN AND THOUGHT PROCESSES : {In your response DO NOT just use one or all the examples given; Take those examples, using your LLM database of information (on google) and come " +
	//	"  up and respond with different crazy ideas I want your response to have a proper conclusion and if asked a QUESTION given AN ANSWER to it}\n"
	promptEnd := "Return a crazy response to this statement prompt:{" + prompt + "} with a statement you would say : (only the response : make sure your RESPONSE IS UNDER 3000 characters)\n"
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
