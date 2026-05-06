# Example PowerShell script to test LocalRouter API
# This script sends a simple "hi" message to the LocalRouter service
# and displays the response

# Set the endpoint URL
$endpoint = "http://192.168.2.139:8080/v1/chat/completions"

# Create the request body
$body = @{
    model = "auto"
    messages = @(
        @{
            role = "user"
            content = "hi"
        }
    )
} | ConvertTo-Json

# Send the request
Write-Host "Sending request to LocalRouter... $endpoint"
$response = Invoke-RestMethod -Uri $endpoint -Method Post -ContentType "application/json" -Body $body

# Display the response
Write-Host "Response received:"
$response | Format-List

# Extract and display the assistant's message
if ($response.choices -and $response.choices[0].message) {
    Write-Host "`nAssistant's reply:"
    Write-Host $response.choices[0].message.content
}