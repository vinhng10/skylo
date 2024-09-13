# Use Python 3.11 Alpine image as the base image (lighter version of Python)
FROM python:3.11-slim

# Create a non-root user and group
RUN groupadd -r skylo && useradd -r -g skylo skylo

# Set the working directory inside the container to /app
WORKDIR /app

# Copy the requirements.txt file to the working directory
COPY ./requirements.txt /app/requirements.txt

# Install the dependencies listed in requirements.txt with no cache for smaller image size
RUN pip install --no-cache-dir --upgrade -r /app/requirements.txt

# Copy the main application script into the working directory
COPY ./main.py /app/

# Change ownership of the application files to the non-root user
RUN chown -R skylo:skylo /app

# Switch to the non-root user
USER skylo

# Expose the port FastAPI will run on
EXPOSE 8000

# Set the command to run FastAPI app on container startup, exposing it on port 80
CMD ["fastapi", "run", "main.py", "--host", "0.0.0.0", "--port", "8000"]
